/*
Copyright 2026 BUPT AIOps Lab.
*/

// Package controller 实现 VNode 的 Reconciler。
//
// Reconcile 流程概览（详见 docs/architecture.md §3.1）：
//
//  1. 处理删除（finalizer 触发）：patch 邻居移除指向自己的 link，删除 Pod，等 Pod 消失，移除 finalizer。
//  2. 加 finalizer。
//  3. 校验 spec（接口名长度 / link uid 唯一 / role+nodeSelector 一致性）。
//  4. ensure Pod：不存在则按 spec.template 渲染并创建（注入 ownerRef / nodeSelector / labels / init）。
//  5. 漂移检测：containerID / hostIP 变化 → linkStatus 全部置 Pending，让 agent 重建。
//  6. 给跨节点 link 分配 VNI（写到 status.linkStatus[uid].vni）。
//  7. 同步 status.{hostIP, hostNode, containerID, observedGeneration, phase, conditions}。
//  8. 邻居反向触发（hostIP 变化时 enqueue 所有 peer）。
//  9. requeue 周期保险（30s）。
package controller

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	vntopov1alpha1 "github.com/jyblyh/k8s-operator/api/v1alpha1"
	"github.com/jyblyh/k8s-operator/internal/common"
)

// VNodeReconciler reconciles a VNode object.
type VNodeReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// InitImage 控制注入到 Pod 的 vntopo-init 容器使用的镜像。
	InitImage string
}

// +kubebuilder:rbac:groups=vntopo.bupt.site,resources=vnodes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vntopo.bupt.site,resources=vnodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=vntopo.bupt.site,resources=vnodes/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods/status,verbs=get
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Reconcile 是主控制循环。
//
// M1 实装：finalizer / validate / ensurePod / syncStatus。
// M2/M3 中再补：VNI 分配 / 删除联动 / 邻居反向触发 / 漂移自愈。
func (r *VNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var vn vntopov1alpha1.VNode
	if err := r.Get(ctx, req.NamespacedName, &vn); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// ---------- 1. 删除流程 ----------
	if !vn.DeletionTimestamp.IsZero() {
		return r.reconcileDeletion(ctx, &vn)
	}

	// ---------- 2. 加 finalizer ----------
	if !controllerutil.ContainsFinalizer(&vn, common.Finalizer) {
		controllerutil.AddFinalizer(&vn, common.Finalizer)
		if err := r.Update(ctx, &vn); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// ---------- 3. 校验 ----------
	if err := r.validateSpec(&vn); err != nil {
		logger.Info("spec validation failed", "err", err.Error())
		return r.recordValidationFailure(ctx, &vn, err)
	}
	r.recordValidationSuccess(&vn)

	// ---------- 4. ensure Pod ----------
	pod, err := r.ensurePod(ctx, &vn)
	if err != nil {
		return ctrl.Result{}, err
	}
	if pod == nil {
		// Pod 刚被创建（或并发已存在），等下一次 reconcile 拿到。
		return ctrl.Result{RequeueAfter: time.Duration(common.DefaultRequeueShortSec) * time.Second}, nil
	}

	// ---------- 5/7. 漂移 + 同步 status ----------
	if err := r.syncStatus(ctx, &vn, pod); err != nil {
		return ctrl.Result{}, err
	}

	// ---------- 6. VNI 分配（M2） ----------
	// TODO(M2): r.allocateVNIForCrossHostLinks(ctx, &vn)

	// ---------- 8. 邻居反向触发（M3） ----------
	// TODO(M3): if hostIPChanged { r.enqueuePeers(...) }

	logger.V(1).Info("reconciled",
		"name", vn.Name, "ns", vn.Namespace,
		"phase", vn.Status.Phase, "node", vn.Status.HostNode)
	return ctrl.Result{RequeueAfter: time.Duration(common.DefaultRequeueLongSec) * time.Second}, nil
}

// reconcileDeletion 处理 VNode 被删除时的 finalizer 流程。
//
// M1 仅实现移除 finalizer；M3 中补齐 patch 邻居 + 等待 Pod 消失。
func (r *VNodeReconciler) reconcileDeletion(ctx context.Context, vn *vntopov1alpha1.VNode) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(vn, common.Finalizer) {
		return ctrl.Result{}, nil
	}

	// TODO(M3): 1) patch 所有 peer 的 spec.links 移除指向 vn 的 entry
	// TODO(M3): 2) delete Pod (显式)，等待消失
	// TODO(M3): 3) 验证 Pod 已不存在再继续

	controllerutil.RemoveFinalizer(vn, common.Finalizer)
	if err := r.Update(ctx, vn); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// recordValidationFailure 把校验失败信息写到 status，phase=Failed，不重试（等用户改 spec）。
func (r *VNodeReconciler) recordValidationFailure(
	ctx context.Context, vn *vntopov1alpha1.VNode, validationErr error,
) (ctrl.Result, error) {
	base := vn.DeepCopy()
	meta.SetStatusCondition(&vn.Status.Conditions, metav1.Condition{
		Type:               common.ConditionValidated,
		Status:             metav1.ConditionFalse,
		Reason:             "ValidationFailed",
		Message:            validationErr.Error(),
		ObservedGeneration: vn.Generation,
		LastTransitionTime: metav1.Now(),
	})
	vn.Status.Phase = vntopov1alpha1.PhaseFailed
	vn.Status.ObservedGeneration = vn.Generation

	if err := r.Status().Patch(ctx, vn, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, err
	}
	// 不重试；用户更新 spec 后 watch 会自然触发新一次 reconcile。
	return ctrl.Result{}, nil
}

// recordValidationSuccess 仅在内存中翻转 Validated condition；真正写盘随 syncStatus 一起做。
func (r *VNodeReconciler) recordValidationSuccess(vn *vntopov1alpha1.VNode) {
	meta.SetStatusCondition(&vn.Status.Conditions, metav1.Condition{
		Type:               common.ConditionValidated,
		Status:             metav1.ConditionTrue,
		Reason:             "Validated",
		Message:            "spec passed validation",
		ObservedGeneration: vn.Generation,
		LastTransitionTime: metav1.Now(),
	})
}

// SetupWithManager 注册 controller 到 manager。
//
// Watch 的对象：
//   - VNode 自身（主对象）
//   - 由 VNode 拥有的 Pod（owns 关系，Pod 事件触发对应 VNode reconcile）
//   - 邻居 VNode 的变更也要通过 EnqueueRequestsFromMapFunc 反向触发（M3 实现）。
//
// 注：controller-runtime v0.11.x 的 Watches 需要 source.Kind 包装；
// MapFunc 不接收 ctx（v0.15+ 才加上）。
func (r *VNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vntopov1alpha1.VNode{}, builder.WithPredicates()).
		Owns(&corev1.Pod{}).
		Watches(
			&source.Kind{Type: &vntopov1alpha1.VNode{}},
			handler.EnqueueRequestsFromMapFunc(r.mapPeerToReconcile),
		).
		Complete(r)
}

// mapPeerToReconcile 当某个 VNode 的 status.hostIP 变化时，触发它所有 peer 的 reconcile。
// M3 中实装；M1 暂返回空切片。
func (r *VNodeReconciler) mapPeerToReconcile(obj client.Object) []reconcile.Request {
	return nil
}
