/*
Copyright 2026 BUPT AIOps Lab.
*/

// Package agent 实现节点级数据平面：
//   - 通过 unix socket jsonrpc 接收 init 容器的 SetupLinks 调用
//   - watch 本节点上 Pod 对应的 VNode，作为 RPC 路径之外的兜底重建
//   - 异步 worker pool 真正执行 veth/vxlan 操作
//   - same-host 用 veth pair（M2 已实现，netlink + netns）
//   - cross-host 用 vxlan + bridge（M3）
//   - 周期 drift 扫描 + 自愈（M3）
package agent

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	vntopov1alpha1 "github.com/jyblyh/k8s-operator/api/v1alpha1"
)

// vnTopologyChanged 是一个自定义 predicate：
//
//   - spec generation 变 → 触发（设备增减、IP 修改、nodeSelector 修改）
//   - status.hostNode 变 → 触发（pod 调度落点变了，跨节点 link 需要重算）
//   - status.hostIP   变 → 触发（节点 IP 变了，VXLAN remote 需要更新）
//   - status.configHash 变 → 触发（M4：controller 渲染了新的 ConfigMap，agent 该 reload）
//   - 其他 status 变化（linkStatus / conditions / phase / serviceReload）→ **不触发**
//
// 为什么需要这个：默认的 GenerationChangedPredicate 只看 spec 的 generation；
// 但 router 类节点 nodeSelector 为空、由 K8s 自由调度，**spec 不变**而它的
// status.hostNode 是后写入的——peer 端如果只看 generation，永远看不到这个
// 变化，必须等 60s drift scan 才能感知。
//
// 同时排除 linkStatus / conditions / serviceReload：agent 自己 patch 这些字段，
// 如果让它们触发 reconcile 就会形成 patch → reconcile → enqueue → patch 的
// 反馈环（M2 时被打爆队列的那个 bug）。
//
// configHash 是 controller 写的，agent 只读不写，没有反馈环顾虑。
type vnTopologyChanged struct{ predicate.Funcs }

func (vnTopologyChanged) Update(e event.UpdateEvent) bool {
	o, ok1 := e.ObjectOld.(*vntopov1alpha1.VNode)
	n, ok2 := e.ObjectNew.(*vntopov1alpha1.VNode)
	if !ok1 || !ok2 {
		return false
	}
	if o.Generation != n.Generation {
		return true
	}
	if o.Status.HostNode != n.Status.HostNode {
		return true
	}
	if o.Status.HostIP != n.Status.HostIP {
		return true
	}
	if o.Status.ConfigHash != n.Status.ConfigHash {
		return true
	}
	return false
}

// Reconciler 在每个 worker 节点上跑，把本节点 Pod 相关的 VNode 事件转换成
// SetupTask 推到 WorkerPool。真正建链由 SetupHandler 完成。
//
// 与 RPC 路径的关系
// =================
//
// init 容器的 RPC SetupLinks 是 **常态触发**：Pod 起来时初始化建链。
//
// reconciler watch 是 **兜底**：
//   - 对端 VNode 后建：本端先建好 link 时对端不存在，Pending；对端创建后
//     reconciler 收到 VNode 事件，把它的所有邻居重新入队
//   - drift 扫描（M3）：周期性 enqueue 本节点全部 VNode，agent 自愈
//
// 因此 reconciler 不直接调 netlink，只负责 enqueue。
type Reconciler struct {
	client.Client

	NodeName  string
	Pool      *WorkerPool
	Predicate func(p *corev1.Pod) bool // 可选：测试用
}

// Reconcile 处理一次 VNode 变更：只把变更对象自身入队。
//
// 邻居（peer）入队由 Watches(VNode) + mapVNodePeerToSelf 路径自动完成；
// 这里不再像早期版本那样手动 for-loop spec.links 入队对端，避免和反向
// watch 路径重复——worker pool 的 sync.Map 去重虽然能兜住，但少一份
// 冗余事件总是更干净。
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("node", r.NodeName)

	var vn vntopov1alpha1.VNode
	if err := r.Get(ctx, req.NamespacedName, &vn); err != nil {
		// 删了：放手让 OwnerReferences/finalizer 走 controller 那边
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 只关心本节点 VNode（status.hostNode 兜底用 nodeSelector）
	if !r.belongsToThisNode(&vn) {
		return ctrl.Result{}, nil
	}

	if err := r.Pool.Enqueue(SetupTask{
		Namespace: vn.Namespace,
		PodName:   vn.Name,
	}); err != nil {
		logger.Error(err, "enqueue task failed; will retry on next reconcile")
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{}, nil
}

// belongsToThisNode 同 SetupHandler 同名方法的简化版，避免循环依赖。
func (r *Reconciler) belongsToThisNode(vn *vntopov1alpha1.VNode) bool {
	if vn.Status.HostNode == r.NodeName {
		return true
	}
	if vn.Spec.NodeSelector != nil {
		if v, ok := vn.Spec.NodeSelector["kubernetes.io/hostname"]; ok && v == r.NodeName {
			return true
		}
	}
	return false
}

// SetupWithManager 注册 watch。三条路径同时跑：
//
//  1. **For(VNode)**：自身 spec / hostNode / hostIP 变化 → reconcile 自身。
//     用 vnTopologyChanged 屏蔽 linkStatus 反馈环。
//
//  2. **Watches(VNode) + mapVNodePeerToSelf**：任意 VNode 的 hostNode/hostIP/spec
//     变化 → 把它的 peer（spec.links 中引用了它的 VNode）入队。这一条专门
//     解决"router 默认调度后 host 节点 agent 必须等 60s drift scan 才知道
//     router 落到哪"的滞后问题——router status.hostNode 一旦写入，host
//     节点的 reconciler 就会立刻把 host VNode 入队，agent 重新建跨节点 link。
//
//  3. **Watches(Pod)**：本节点 Pod 创建/重建 → reconcile 对应 VNode（用同名匹配）。
//
// 注意：controller-runtime v0.11.x 的 Watches 需要 source.Kind 包装；
// MapFunc 不接收 ctx（v0.15+ 才加上）。
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vntopov1alpha1.VNode{},
			builder.WithPredicates(vnTopologyChanged{}),
		).
		Watches(
			&source.Kind{Type: &vntopov1alpha1.VNode{}},
			handler.EnqueueRequestsFromMapFunc(r.mapVNodePeerToSelf),
			builder.WithPredicates(vnTopologyChanged{}),
		).
		Watches(
			&source.Kind{Type: &corev1.Pod{}},
			handler.EnqueueRequestsFromMapFunc(r.mapPodToVNode),
			builder.WithPredicates(podOnThisNode(r.NodeName)),
		).
		Complete(r)
}

// mapPodToVNode：Pod 事件 -> 对应 VNode 的 reconcile request（同名同 namespace）。
func (r *Reconciler) mapPodToVNode(obj client.Object) []reconcile.Request {
	return []reconcile.Request{
		{
			NamespacedName: client.ObjectKey{Namespace: obj.GetNamespace(), Name: obj.GetName()},
		},
	}
}

// mapVNodePeerToSelf 反向映射：当某个 VNode "changed" 的 hostNode/hostIP/spec
// 变化时，找出 namespace 内所有把 changed.Name 列为 peer 的 VNode，把它们入队。
//
// 实现走 informer cache（r.List），不会打到 apiserver；mapping 函数本身在
// controller-runtime 的 watch 处理协程上跑，要避免阻塞——cache list 是 O(N)
// 内存遍历，N 是本 namespace 内 VNode 数量，几十几百级别完全 OK。
func (r *Reconciler) mapVNodePeerToSelf(obj client.Object) []reconcile.Request {
	changed, ok := obj.(*vntopov1alpha1.VNode)
	if !ok {
		return nil
	}
	var list vntopov1alpha1.VNodeList
	if err := r.List(context.Background(), &list, client.InNamespace(changed.Namespace)); err != nil {
		return nil
	}
	var out []reconcile.Request
	for i := range list.Items {
		v := &list.Items[i]
		if v.Name == changed.Name {
			// 自身已经被 For 路径覆盖，不重复
			continue
		}
		for _, l := range v.Spec.Links {
			if l.PeerPod == changed.Name {
				out = append(out, reconcile.Request{
					NamespacedName: client.ObjectKey{Namespace: v.Namespace, Name: v.Name},
				})
				break
			}
		}
	}
	return out
}
