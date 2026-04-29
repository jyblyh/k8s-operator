/*
Copyright 2026 BUPT AIOps Lab.
*/

package agent

import (
	"context"
	"fmt"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	vntopov1alpha1 "github.com/jyblyh/k8s-operator/api/v1alpha1"
)

// patchLinkStatus 用最新的 results 覆盖 vn.status.linkStatus 子资源，并按需更新
// LinksConverged Condition。
//
// 我们用 **乐观重试 + Status().Update()**：
//   - 先 Get 最新一份 vn
//   - 合并 linkStatus（按 uid merge）
//   - Status().Update() 写回，遇到 Conflict 退避重试
//
// 之所以不用 patch RFC7396 / strategic merge：linkStatus 本来就是数组，
// merge 复杂且有边界问题（旧条目要不要清理？）。直接 Update 简单清晰。
func (h *SetupHandler) patchLinkStatus(
	ctx context.Context,
	vn *vntopov1alpha1.VNode,
	results []vntopov1alpha1.LinkStatus,
) error {
	const maxAttempts = 5
	key := types.NamespacedName{Namespace: vn.Namespace, Name: vn.Name}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// patch 前一定要拿最新一份 spec/status，避免 conflict；走 Reader 直查 apiserver。
		var latest vntopov1alpha1.VNode
		if err := h.Reader.Get(ctx, key, &latest); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("get vnode for status: %w", err)
		}

		// 用 results 覆盖（按 uid 排序，输出稳定）
		sort.SliceStable(results, func(i, j int) bool { return results[i].UID < results[j].UID })
		latest.Status.LinkStatus = mergeLinkStatusByUID(latest.Status.LinkStatus, results)

		// 顺便把 LinksConverged condition 更新一下
		setLinksConvergedCondition(&latest, results)

		if err := h.Client.Status().Update(ctx, &latest); err != nil {
			if apierrors.IsConflict(err) {
				// 别人改了，退避后重试
				continue
			}
			return fmt.Errorf("update vnode status: %w", err)
		}
		return nil
	}
	return fmt.Errorf("update vnode status: exhausted %d retries", maxAttempts)
}

// mergeLinkStatusByUID 用 newer 中的 LinkStatus 覆盖 prev 中相同 uid 的条目；
// 不在 newer 中的旧条目保持原样（防止误清理跨节点 link 的状态）。
func mergeLinkStatusByUID(prev, newer []vntopov1alpha1.LinkStatus) []vntopov1alpha1.LinkStatus {
	idx := map[int64]int{}
	for i, s := range prev {
		idx[s.UID] = i
	}
	out := append([]vntopov1alpha1.LinkStatus(nil), prev...)
	for _, s := range newer {
		if i, ok := idx[s.UID]; ok {
			out[i] = s
		} else {
			out = append(out, s)
			idx[s.UID] = len(out) - 1
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out
}

// setLinksConvergedCondition 把汇总的链路状态写到 conditions[type=LinksConverged]。
//
// 语义：所有本节点能处理的 link 全部 Established → status=True；
// 任一 Error → status=False，reason=LinkError；
// 否则（Pending/跨节点跳过）→ status=False，reason=Progressing。
func setLinksConvergedCondition(vn *vntopov1alpha1.VNode, results []vntopov1alpha1.LinkStatus) {
	totalLocal := 0
	established := 0
	hasError := false
	var firstErrMsg string

	for _, s := range results {
		// 跨节点 link M2 不处理，不计入收敛与否的判断
		if s.Mode == vntopov1alpha1.LinkModeVXLAN {
			continue
		}
		totalLocal++
		switch s.State {
		case vntopov1alpha1.LinkStateEstablished:
			established++
		case vntopov1alpha1.LinkStateError:
			hasError = true
			if firstErrMsg == "" {
				firstErrMsg = s.LastError
			}
		}
	}

	cond := metav1.Condition{
		Type:               "LinksConverged",
		ObservedGeneration: vn.Generation,
		LastTransitionTime: metav1.NewTime(time.Now()),
	}
	switch {
	case hasError:
		cond.Status = metav1.ConditionFalse
		cond.Reason = "LinkError"
		cond.Message = firstErrMsg
	case totalLocal > 0 && established == totalLocal:
		cond.Status = metav1.ConditionTrue
		cond.Reason = "AllVethEstablished"
		cond.Message = fmt.Sprintf("%d/%d local veth links established", established, totalLocal)
	default:
		cond.Status = metav1.ConditionFalse
		cond.Reason = "Progressing"
		cond.Message = fmt.Sprintf("%d/%d local veth links established", established, totalLocal)
	}

	meta.SetStatusCondition(&vn.Status.Conditions, cond)
}

// nowMetav1 返回当前时间的 metav1.Time，避免到处写 metav1.NewTime(time.Now())。
func nowMetav1() metav1.Time { return metav1.NewTime(time.Now()) }
