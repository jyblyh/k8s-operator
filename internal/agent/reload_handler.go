/*
Copyright 2026 BUPT AIOps Lab.
*/

package agent

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	vntopov1alpha1 "github.com/jyblyh/k8s-operator/api/v1alpha1"
	"github.com/jyblyh/k8s-operator/internal/roleinjector"
)

// reloadIfNeeded 在每次 setup pass 末尾被调用：
//
//   - 比较 vn.Status.ConfigHash 与 vn.Status.ServiceReload.ObservedHash
//   - 不一致 → docker exec 主容器执行 RoleInjector.ReloadCommand
//   - 把执行结果（Success / Failed / NotApplicable）patch 到 status.serviceReload
//
// 设计要点：
//
//  1. 复用 RoleInjector 拿 ReloadCommand：agent 与 controller 共享 internal/roleinjector
//     这个 pure-function 包，不引入新的 IPC。
//
//  2. 只对落在本节点的 VNode 做。跨节点的 reload 由对端 agent 自己处理。
//     （在 Handle 里已经 belongsToThisNode 拦过一遍。）
//
//  3. 失败重试：exec 出错时把 state=Failed 写下，下次 reconcile（无论是 drift
//     scan 还是 controller 再 patch configHash）会重新对比 hash 重试；不像
//     link setup 那样靠 worker 自己重试，避免 ConfigMap 热更新和链路重建相互
//     影响。
//
//  4. **首次** 出现 ConfigHash 时（ServiceReload==nil），立刻触发一次 reload，
//     即使容器刚起来已经主动 load 过一遍——这是为了应对"用户改 spec 后
//     controller 立刻重渲染 ConfigMap 但 Pod 没重建"的常见路径，多 reload 一次
//     成本很低（frrinit.sh restart < 1s）。
//
//  5. 主容器还没起来时 docker exec 会失败，state=Pending 即可，下次再来。
type reloadAction string

const (
	reloadSkip          reloadAction = "skip"           // hash 一致，无事可做
	reloadNotApplicable reloadAction = "not_applicable" // role 不需要 reload
	reloadDoExec        reloadAction = "exec"           // 真正去 docker exec
)

// decideReloadAction 决定本次该做什么。pure logic，便于单测。
func decideReloadAction(vn *vntopov1alpha1.VNode, reloadCmd []string) reloadAction {
	if vn.Status.ConfigHash == "" {
		// controller 还没渲染过 ConfigMap，比如 spec 没 RoleConfig 也没 ExtraConfigMaps
		return reloadSkip
	}
	// hash 已稳定且我们已经 reload 过同一份 → 跳过
	if vn.Status.ServiceReload != nil &&
		vn.Status.ServiceReload.ObservedHash == vn.Status.ConfigHash &&
		// Failed 状态也允许重试一次（hash 没变但上次失败）
		vn.Status.ServiceReload.State == vntopov1alpha1.ReloadSuccess {
		return reloadSkip
	}
	// NotApplicable 状态：role 不需要 reload，hash 不变就跳；hash 变了下面会更新
	if vn.Status.ServiceReload != nil &&
		vn.Status.ServiceReload.ObservedHash == vn.Status.ConfigHash &&
		vn.Status.ServiceReload.State == vntopov1alpha1.ReloadNotApplicable {
		return reloadSkip
	}
	if len(reloadCmd) == 0 {
		return reloadNotApplicable
	}
	return reloadDoExec
}

// reloadIfNeeded 真正执行 reload（如需）。返回 nil 不代表 reload 成功，只代表
// 没有需要在 Handler 层升级抛出的 error；reload 的 Success/Failed 通过
// status.serviceReload 表达。
func (h *SetupHandler) reloadIfNeeded(ctx context.Context, vn *vntopov1alpha1.VNode) error {
	logger := log.FromContext(ctx).WithValues(
		"namespace", vn.Namespace, "pod", vn.Name, "node", h.NodeName)

	// 用 pure function 算一次 ReloadCommand
	inject, err := roleinjector.For(vn.Spec.Role).Inject(vn)
	if err != nil {
		// 设计上 Inject 不会出错；真出错就当 NotApplicable 走兜底
		logger.V(1).Info("role inject for reload failed; treating as not-applicable", "err", err)
		inject = &roleinjector.RoleInject{}
	}

	switch decideReloadAction(vn, inject.ReloadCommand) {
	case reloadSkip:
		return nil

	case reloadNotApplicable:
		return h.patchReloadStatus(ctx, vn, &vntopov1alpha1.ServiceReloadStatus{
			ObservedHash: vn.Status.ConfigHash,
			State:        vntopov1alpha1.ReloadNotApplicable,
		})

	case reloadDoExec:
		return h.execReload(ctx, vn, inject.ReloadCommand)
	}

	return nil
}

// execReload 找业务容器 → docker exec → 写 ServiceReload 状态。
func (h *SetupHandler) execReload(
	ctx context.Context, vn *vntopov1alpha1.VNode, cmd []string,
) error {
	logger := log.FromContext(ctx).WithValues(
		"namespace", vn.Namespace, "pod", vn.Name)

	if h.Docker == nil {
		// 测试 / dry-run 路径：没注入 docker client 时，只写一次 NotApplicable
		// 让流程不会无限 reconcile。
		return h.patchReloadStatus(ctx, vn, &vntopov1alpha1.ServiceReloadStatus{
			ObservedHash: vn.Status.ConfigHash,
			State:        vntopov1alpha1.ReloadNotApplicable,
			Message:      "agent has no docker client",
			Command:      cmd,
		})
	}

	containerName := mainContainerName(vn)
	cid, err := h.Docker.FindContainerID(ctx, vn.Namespace, vn.Name, containerName)
	if err != nil {
		// 主容器还没起来：标 Pending（不是 Failed），下次再试
		logger.V(1).Info("reload: business container not yet ready", "err", err.Error())
		return h.patchReloadStatus(ctx, vn, &vntopov1alpha1.ServiceReloadStatus{
			ObservedHash: vn.Status.ConfigHash,
			State:        vntopov1alpha1.ReloadPending,
			Command:      cmd,
			Message:      truncateMsg(err.Error()),
		})
	}

	logger.Info("reload: docker exec", "container", containerName, "cmd", cmd)
	res, err := h.Docker.Exec(ctx, cid, cmd)
	if err != nil {
		logger.Info("reload: exec error", "err", err.Error())
		return h.patchReloadStatus(ctx, vn, &vntopov1alpha1.ServiceReloadStatus{
			ObservedHash: vn.Status.ConfigHash,
			State:        vntopov1alpha1.ReloadFailed,
			Command:      cmd,
			Message:      truncateMsg(fmt.Sprintf("exec error: %v", err)),
		})
	}
	if res.ExitCode != 0 {
		// 命令本身退出码非 0：service reload 失败
		logger.Info("reload: exec non-zero",
			"exit", res.ExitCode,
			"stderr", string(res.Stderr))
		return h.patchReloadStatus(ctx, vn, &vntopov1alpha1.ServiceReloadStatus{
			ObservedHash: vn.Status.ConfigHash,
			State:        vntopov1alpha1.ReloadFailed,
			Command:      cmd,
			Message: truncateMsg(fmt.Sprintf("exit=%d stderr=%s",
				res.ExitCode, strings.TrimSpace(string(res.Stderr)))),
		})
	}
	logger.Info("reload: exec ok", "stdout_bytes", len(res.Stdout))
	return h.patchReloadStatus(ctx, vn, &vntopov1alpha1.ServiceReloadStatus{
		ObservedHash: vn.Status.ConfigHash,
		State:        vntopov1alpha1.ReloadSuccess,
		Command:      cmd,
	})
}

// patchReloadStatus 把 newReload patch 到 vn.status.serviceReload。
//
// 用 MergeFrom patch 而不是全量 Update：
//   - 只动 status.serviceReload，不会把刚被 controller 写好的 configHash 覆盖
//   - 不会和 patchLinkStatus 互斥（不同字段）
func (h *SetupHandler) patchReloadStatus(
	ctx context.Context, vn *vntopov1alpha1.VNode, newReload *vntopov1alpha1.ServiceReloadStatus,
) error {
	// 写时间戳：仅在 state 实际变化（或首次写入）时刷新，避免 patch 噪音
	old := vn.Status.ServiceReload
	if old == nil || old.State != newReload.State || old.ObservedHash != newReload.ObservedHash {
		now := metav1.Now()
		newReload.LastTransitionTime = &now
	} else if old != nil {
		newReload.LastTransitionTime = old.LastTransitionTime
	}

	if old != nil &&
		old.State == newReload.State &&
		old.ObservedHash == newReload.ObservedHash &&
		old.Message == newReload.Message {
		// 完全没变化，不发 API 请求
		return nil
	}

	base := vn.DeepCopy()
	vn.Status.ServiceReload = newReload
	return h.Client.Status().Patch(ctx, vn, client.MergeFrom(base))
}

// mainContainerName 取 VNode.template.containers[0] 的名字；为空时返回空，
// 调用方会用"任一业务容器"兜底。
func mainContainerName(vn *vntopov1alpha1.VNode) string {
	cs := vn.Spec.Template.Spec.Containers
	if len(cs) == 0 {
		return ""
	}
	return cs[0].Name
}

// truncateMsg 把任意字符串截到 status 字段安全长度，防止 etcd 单字段过大。
func truncateMsg(s string) string {
	const maxLen = 1024
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...(truncated)"
}
