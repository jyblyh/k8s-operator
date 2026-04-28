/*
Copyright 2026 BUPT AIOps Lab.
*/

// Package agent 实现节点级数据平面：
//   - watch 本节点 Pod 对应的 VNode
//   - same-host 用 veth pair（复用 koko）
//   - cross-host 用 vxlan + bridge
//   - 周期 drift 扫描 + 自愈
//   - 通过 unix socket gRPC 接收 init 容器调用
package agent

import (
	"context"
	"net"

	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	vntopov1alpha1 "github.com/jyblyh/k8s-operator/api/v1alpha1"
)

// Reconciler 在每个 worker 节点上跑，处理本节点 Pod 的链路 diff & apply。
type Reconciler struct {
	client.Client

	NodeName      string
	UnderlayIface string
}

// Reconcile 处理一次 VNode 变更。
//
// M0 仅打日志骨架；M1/M2 中接入实际的 veth/vxlan 操作。
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("node", r.NodeName)

	var vn vntopov1alpha1.VNode
	if err := r.Get(ctx, req.NamespacedName, &vn); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 只处理 Pod 调度到本节点的 VNode。
	if vn.Status.HostNode != r.NodeName {
		return ctrl.Result{}, nil
	}

	// TODO(M1):
	//   desired := vn.Spec.Links
	//   actual  := scanLocalDevices(vn)
	//   for link in desired:
	//       peer = get(vn.namespace, link.peer_pod)
	//       if peer.hostNode == r.NodeName: makeVeth(...)
	//       else:                            makeVXLAN(..., vni from status)
	//   清理孤儿设备
	//   patch status.linkStatus[*]

	logger.V(1).Info("agent reconciled", "vnode", vn.Name)
	return ctrl.Result{}, nil
}

// SetupWithManager 注册 watch；通过 fieldSelector 限定只接收本节点 Pod 对应的 VNode。
//
// 注意：controller-runtime v0.11.x 的 Watches 需要 source.Kind 包装；
// MapFunc 不接收 ctx（v0.15+ 才加上）。
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vntopov1alpha1.VNode{}, builder.WithPredicates()).
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

// 为后续 cache 配置预留：让 mgr 给 Pod 在 spec.nodeName 上建索引。
//
// 在 cmd/agent/main.go 里 manager 启动前可：
//
//	mgr.GetCache().IndexField(ctx, &corev1.Pod{}, "spec.nodeName",
//	    func(o client.Object) []string {
//	        return []string{o.(*corev1.Pod).Spec.NodeName}
//	    })
//
// M1 中再启用，避免 M0 启动期失败。
var _ = fields.OneTermEqualSelector

// RunGRPCServer 在给定 listener 上跑 unix socket gRPC server，接收 init 容器调用。
//
// M0 仅注册一个空 server；M1 中实现 SetupLink 接口（兼容 p2pnet 协议）。
func RunGRPCServer(lis net.Listener, r *Reconciler) error {
	srv := grpc.NewServer()
	// TODO(M1): netservicepb.RegisterLocalServer(srv, &grpcHandler{reconciler: r})
	return srv.Serve(lis)
}
