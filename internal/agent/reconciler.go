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
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	vntopov1alpha1 "github.com/jyblyh/k8s-operator/api/v1alpha1"
)

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

// Reconcile 处理一次 VNode 变更：把变更对象自身 + 它的邻居全部入队。
//
// 入队的是 Pod-级任务，handler 自己读最新 spec，所以这里不需要传 link 详情。
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
		// queue 满 → 不阻塞 controller-runtime 队列，下次 reconcile 再试
		logger.Error(err, "enqueue task failed; will retry on next reconcile")
		return ctrl.Result{Requeue: true}, nil
	}

	// 邻居：当前 vn 状态变化（比如刚被调度）会影响邻居 LinksConverged，
	// 把同 namespace 内 spec.links.peer_pod 引用到 vn 的 VNode 也入队。
	// 简化做法：只入队 vn.spec.links 的对端；对面如果也指向我，他自己 reconcile
	// 会走对称路径。
	for _, link := range vn.Spec.Links {
		_ = r.Pool.Enqueue(SetupTask{
			Namespace: vn.Namespace,
			PodName:   link.PeerPod,
		})
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

// SetupWithManager 注册 watch。
//
// 关键：VNode watch 只在 **spec generation 变化** 时触发；status 变化不触发。
// 这一条非常重要——agent 自己 patch status.linkStatus 也会引起 watch 事件，
// 如果不过滤就会形成 patch → reconcile → enqueue → patch 的反馈环，导致
// worker 队列被相同任务灌爆（出现过 cap=256 全部装满的 bug）。
//
// 真正建链需要重新触发的事件：
//   - VNode spec 改动（Generation 变化）→ 由 GenerationChangedPredicate 放行
//   - Pod 创建/重建（containerID 变化）→ 由 Pod watch + podOnThisNode 放行
//
// 注意：controller-runtime v0.11.x 的 Watches 需要 source.Kind 包装；
// MapFunc 不接收 ctx（v0.15+ 才加上）。
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vntopov1alpha1.VNode{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
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
