/*
Copyright 2026 BUPT AIOps Lab.
*/

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vntopov1alpha1 "github.com/jyblyh/k8s-operator/api/v1alpha1"
	"github.com/jyblyh/k8s-operator/internal/common"
	"github.com/jyblyh/k8s-operator/internal/roleinjector"
)

// =============================================================================
//  validateSpec
// =============================================================================

// validRoles 是 VNode role 字段允许的取值集合。
// CRD enum 已强制一次，这里二次校验防 webhook 缺位。
var validRoles = map[vntopov1alpha1.VNodeRole]struct{}{
	vntopov1alpha1.RoleHost: {}, vntopov1alpha1.RoleSW: {}, vntopov1alpha1.RoleASW: {},
	vntopov1alpha1.RoleCSW: {}, vntopov1alpha1.RoleR: {}, vntopov1alpha1.RoleFW: {},
	vntopov1alpha1.RoleDHCP: {}, vntopov1alpha1.RoleDNS: {}, vntopov1alpha1.RoleWS: {},
}

// validateSpec 在 reconcile 早期做基础校验，失败会让 phase=Failed 直到用户修正 spec。
//
// M1 校验项：
//   - role 在白名单
//   - 非路由器且声明了 dataCenter 时，必须填 nodeSelector 或 affinity
//   - 同 VNode 内 link.uid 唯一
//   - link.local_intf / peer_intf 合法（长度 + 字符集）
//
// 跨对象一致性（同 dataCenter nodeSelector 一致 / 对端 link 对称）由 webhook 兜底，M3 实装。
func (r *VNodeReconciler) validateSpec(vn *vntopov1alpha1.VNode) error {
	if _, ok := validRoles[vn.Spec.Role]; !ok {
		return fmt.Errorf("invalid role %q", vn.Spec.Role)
	}

	if vn.Spec.Role != vntopov1alpha1.RoleR && vn.Spec.DataCenter != "" {
		if len(vn.Spec.NodeSelector) == 0 && vn.Spec.Affinity == nil {
			return fmt.Errorf("role=%s with dataCenter=%q must specify nodeSelector or affinity",
				vn.Spec.Role, vn.Spec.DataCenter)
		}
	}

	seen := map[int64]struct{}{}
	for i, link := range vn.Spec.Links {
		if _, dup := seen[link.UID]; dup {
			return fmt.Errorf("links[%d]: duplicated uid %d in same VNode", i, link.UID)
		}
		seen[link.UID] = struct{}{}

		if err := common.ValidateIntfName(link.LocalIntf); err != nil {
			return fmt.Errorf("links[%d].local_intf: %w", i, err)
		}
		if err := common.ValidateIntfName(link.PeerIntf); err != nil {
			return fmt.Errorf("links[%d].peer_intf: %w", i, err)
		}
		if link.PeerPod == "" {
			return fmt.Errorf("links[%d].peer_pod must be set", i)
		}
		if link.UID < 1 {
			return fmt.Errorf("links[%d].uid must be >= 1", i)
		}
	}

	return nil
}

// =============================================================================
//  ensurePod
// =============================================================================

// ensurePod 确保 vn 对应的 Pod 存在并被本 VNode 拥有。
//
// 返回值：
//   - (nil, nil) ：Pod 刚刚被创建（或并发抢建），下一次 reconcile 再拿
//   - (pod, nil) ：Pod 已存在并且属于本 vn
//   - (_,   err) ：出错
//
// Pod 的渲染逻辑见 pod_renderer.go。这里不做 update —— Pod 模板对存量 Pod 不可变，
// spec 修改只对未来重建的 Pod 生效。漂移检测交给 syncStatus 处理。
func (r *VNodeReconciler) ensurePod(
	ctx context.Context, vn *vntopov1alpha1.VNode, inject *roleinjector.RoleInject,
) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	err := r.Get(ctx, client.ObjectKey{Namespace: vn.Namespace, Name: vn.Name}, pod)
	if err == nil {
		if !isOwnedByVNode(pod, vn) {
			return nil, fmt.Errorf("pod %s/%s already exists but is not owned by VNode %s (uid=%s)",
				vn.Namespace, vn.Name, vn.Name, vn.UID)
		}
		return pod, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}

	rendered, err := renderPod(vn, r.InitImage, r.Scheme, inject)
	if err != nil {
		return nil, fmt.Errorf("renderPod: %w", err)
	}
	if err := r.Create(ctx, rendered); err != nil {
		// 并发或缓存延迟：另一次 reconcile 已经建过；放任下一轮 Get 拿到即可
		if apierrors.IsAlreadyExists(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("create pod: %w", err)
	}
	return nil, nil
}

func isOwnedByVNode(pod *corev1.Pod, vn *vntopov1alpha1.VNode) bool {
	for _, ow := range pod.OwnerReferences {
		if ow.UID == vn.UID && ow.Kind == "VNode" {
			return true
		}
	}
	return false
}

// =============================================================================
//  syncStatus
// =============================================================================

// syncStatus 把 Pod 的运行时信息回填到 VNode.status，并刷新 phase / conditions。
//
// 漂移检测：sandbox containerID 变化（pod 被重建） → 把所有 linkStatus.state 设为 Pending，
// 让 agent 在下次 reconcile 重建链路。M2 / M3 中 agent 会真正消费这个信号。
//
// configHash：M4 引入。控制器渲染并 ensure ConfigMaps 后写入，agent 监听
// 到它和 status.serviceReload.observedHash 不一致时执行 reload 命令。
//
// 写回方式：与 base 做语义比较，无变化时跳过 API 调用，避免无谓写放大。
func (r *VNodeReconciler) syncStatus(
	ctx context.Context, vn *vntopov1alpha1.VNode, pod *corev1.Pod, configHash string,
) error {
	base := vn.DeepCopy()

	vn.Status.ObservedGeneration = vn.Generation
	vn.Status.HostIP = pod.Status.HostIP
	vn.Status.HostNode = pod.Spec.NodeName
	vn.Status.SrcIP = pod.Status.PodIP
	vn.Status.ConfigHash = configHash

	// 漂移检测：取第一个非 init 容器的 ID（足够唯一标识当前 sandbox 实例）。
	if newCID := primaryContainerID(pod); newCID != "" && newCID != vn.Status.ContainerID {
		if vn.Status.ContainerID != "" {
			// 真的重建了（不是首次设置） → 通知 agent 重建链路
			markAllLinksPending(&vn.Status)
		}
		vn.Status.ContainerID = newCID
	}

	vn.Status.Phase = derivePhase(vn, pod)

	meta.SetStatusCondition(&vn.Status.Conditions, metav1.Condition{
		Type:               common.ConditionPodReady,
		Status:             podReadyConditionStatus(pod),
		Reason:             "PodStatusObserved",
		Message:            fmt.Sprintf("pod %s phase=%s", pod.Name, pod.Status.Phase),
		ObservedGeneration: vn.Generation,
		LastTransitionTime: metav1.Now(),
	})

	// LinksConverged 在 M2/M3 由 agent 真正建链后翻转；M1 暂时按 "无 link 也算 converged" 处理。
	convergedStatus := metav1.ConditionFalse
	convergedReason := "WaitingForAgent"
	if len(vn.Spec.Links) == 0 {
		convergedStatus = metav1.ConditionTrue
		convergedReason = "NoLinksDeclared"
	}
	meta.SetStatusCondition(&vn.Status.Conditions, metav1.Condition{
		Type:               common.ConditionLinksConverged,
		Status:             convergedStatus,
		Reason:             convergedReason,
		Message:            "managed by vntopo-agent (M1 placeholder)",
		ObservedGeneration: vn.Generation,
		LastTransitionTime: metav1.Now(),
	})

	if equality.Semantic.DeepEqual(base.Status, vn.Status) {
		return nil
	}
	return r.Status().Patch(ctx, vn, client.MergeFrom(base))
}

// derivePhase 综合 Pod 状态 + 链路收敛状态，给出 VNode 整体 phase。
//
// M1 简化版（agent 还没建链）：直接以 Pod phase 为主，链路状态留给 M2 接入。
func derivePhase(vn *vntopov1alpha1.VNode, pod *corev1.Pod) vntopov1alpha1.VNodePhase {
	if pod == nil {
		return vntopov1alpha1.PhaseCreating
	}
	switch pod.Status.Phase {
	case corev1.PodPending:
		return vntopov1alpha1.PhasePending
	case corev1.PodFailed, corev1.PodSucceeded:
		// 仿真节点不应自然结束，落到这两种状态都算异常。
		return vntopov1alpha1.PhaseFailed
	case corev1.PodRunning:
		if isPodReady(pod) {
			// TODO(M2): 这里要再综合 vn.Status.LinkStatus，
			//           只有所有 link.state==Established 才算 Ready，否则 Degraded。
			return vntopov1alpha1.PhaseReady
		}
		return vntopov1alpha1.PhasePending
	}
	return vntopov1alpha1.PhasePending
}

// =============================================================================
//  helpers
// =============================================================================

// primaryContainerID 返回 Pod 主容器（非 init）的运行时 ID。
// 若主容器尚未启动则返回空串。
func primaryContainerID(pod *corev1.Pod) string {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.ContainerID != "" {
			return cs.ContainerID
		}
	}
	return ""
}

func isPodReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func podReadyConditionStatus(pod *corev1.Pod) metav1.ConditionStatus {
	if pod == nil {
		return metav1.ConditionUnknown
	}
	if isPodReady(pod) {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

func markAllLinksPending(s *vntopov1alpha1.VNodeStatus) {
	for i := range s.LinkStatus {
		s.LinkStatus[i].State = vntopov1alpha1.LinkStatePending
	}
}
