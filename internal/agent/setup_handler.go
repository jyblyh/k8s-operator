/*
Copyright 2026 BUPT AIOps Lab.
*/

package agent

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	vntopov1alpha1 "github.com/jyblyh/k8s-operator/api/v1alpha1"
	"github.com/jyblyh/k8s-operator/internal/common"
)

// SetupHandler 是 worker / reconciler 共用的核心：给定一个本节点 Pod，
// 把它在 spec.links 中声明的所有链路（同节点 + 跨节点）建好，
// 并把结果写回 status。
//
// 设计原则
//
//   - **以本端 Pod 为单位驱动**：每次只处理 task.Pod 的 links，不主动写
//     对端 VNode 的 status——对端有自己的 init 容器/reconciler 会触发自己
//     的 SetupHandler 来写。这样两端权限严格分离，避免 patch 冲突。
//
//   - **同节点 link 走 veth**（M2）；**跨节点 link 走 P2P VXLAN**（M3）。
//     双方各起一个 vxlan 设备，VNI 由 ComputeVNI(namespace, uid) 决定，
//     无需协商。
//
//   - **幂等**：MakeVethPair / MakeVxlanLink 自带幂等检查；handler 自身
//     也容忍部分失败重试。
type SetupHandler struct {
	// Client 是 cache-backed client（来自 mgr.GetClient）。用于写：Status().Update。
	Client client.Client

	// Reader 是 cache-bypass 直连 apiserver 客户端（来自 mgr.GetAPIReader）。
	// 用于读：避免 agent 启动早期 cache 还没 synced 时就被 init 容器的 RPC
	// 触发，Get 拿不到 VNode 误以为已删。
	Reader client.Reader

	NodeName string
	Netns    *PodNetns
	Locks    *LinkLocks

	// Underlay 描述本节点跨节点 VXLAN 走哪张网卡、什么 src IP、什么 MTU。
	// 由 cmd/agent 启动时探测一次后注入。
	Underlay *Underlay

	// NodeIP 用来解析对端 Node 的 InternalIP（vxlan remote）。
	NodeIP *NodeIPResolver
}

// Handle 是 worker pool 调用的入口。返回 error 让 worker 走重试逻辑。
func (h *SetupHandler) Handle(ctx context.Context, task SetupTask) error {
	logger := log.FromContext(ctx).
		WithValues("namespace", task.Namespace, "pod", task.PodName, "node", h.NodeName)

	// 1) 取本端 VNode（用 Reader 直连 apiserver，避免 cache 未 sync 时假性 NotFound）
	var vn vntopov1alpha1.VNode
	key := types.NamespacedName{Namespace: task.Namespace, Name: task.PodName}
	if err := h.Reader.Get(ctx, key, &vn); err != nil {
		if apierrors.IsNotFound(err) {
			// VNode 已删，没事可做
			return nil
		}
		return fmt.Errorf("get local vnode: %w", err)
	}

	// 2) 校验 VNode 真的归本节点处理
	if !h.belongsToThisNode(&vn) {
		logger.V(1).Info("vnode not on this node, skip")
		return nil
	}

	// 3) 解析本 Pod 的 netns 路径（贯穿整个 handler 复用）
	localNs, err := h.Netns.LookupPath(ctx, vn.Namespace, vn.Name)
	if err != nil {
		return fmt.Errorf("lookup local netns: %w", err)
	}

	// 4) 逐条 link 处理
	results := make([]vntopov1alpha1.LinkStatus, 0, len(vn.Spec.Links))
	for _, link := range vn.Spec.Links {
		ls := h.handleOneLink(ctx, &vn, link, localNs)
		results = append(results, ls)
	}

	// 5) 写回 status.linkStatus
	if err := h.patchLinkStatus(ctx, &vn, results); err != nil {
		return fmt.Errorf("patch linkStatus: %w", err)
	}

	logger.Info("setup pass complete",
		"links", len(results), "queued_for_ms", time.Since(task.EnqueuedAt).Milliseconds())
	return nil
}

// belongsToThisNode 判断 vn 是不是落在本节点。
//
// 优先看 status.hostNode（controller 已经回写过）；如果还没回写就退而求其次
// 比对 spec.nodeSelector["kubernetes.io/hostname"]——同 DC 同节点的硬约束。
func (h *SetupHandler) belongsToThisNode(vn *vntopov1alpha1.VNode) bool {
	if vn.Status.HostNode == h.NodeName {
		return true
	}
	if vn.Spec.NodeSelector != nil {
		if v, ok := vn.Spec.NodeSelector["kubernetes.io/hostname"]; ok && v == h.NodeName {
			return true
		}
	}
	return false
}

// peerNodeOf 推断对端 VNode 跑在哪个 K8s 节点上。
// 优先 status.hostNode；fallback spec.nodeSelector。
func (h *SetupHandler) peerNodeOf(peer *vntopov1alpha1.VNode) string {
	if peer.Status.HostNode != "" {
		return peer.Status.HostNode
	}
	if peer.Spec.NodeSelector != nil {
		return peer.Spec.NodeSelector["kubernetes.io/hostname"]
	}
	return ""
}

// handleOneLink 处理单条 link：找对端 → 同/跨节点分流 → 同节点建 veth / 跨节点建 vxlan。
func (h *SetupHandler) handleOneLink(
	ctx context.Context,
	localVN *vntopov1alpha1.VNode,
	link vntopov1alpha1.LinkSpec,
	localNs string,
) vntopov1alpha1.LinkStatus {
	logger := log.FromContext(ctx).WithValues(
		"namespace", localVN.Namespace, "local_pod", localVN.Name,
		"peer_pod", link.PeerPod, "uid", link.UID)

	st := vntopov1alpha1.LinkStatus{
		UID:     link.UID,
		PeerPod: link.PeerPod,
		Mode:    vntopov1alpha1.LinkModeVeth,
		State:   vntopov1alpha1.LinkStatePending,
	}

	// 取对端 VNode（同样走 Reader，对端可能刚被 controller 创建，cache 慢半拍）
	var peer vntopov1alpha1.VNode
	peerKey := types.NamespacedName{Namespace: localVN.Namespace, Name: link.PeerPod}
	if err := h.Reader.Get(ctx, peerKey, &peer); err != nil {
		// 对端还没创建是常态（双方 init 几乎同时 SetupLinks），不视作硬错误
		// 留 Pending，下次 reconcile 再试
		logger.V(1).Info("peer not yet available, link pending", "err", err)
		st.State = vntopov1alpha1.LinkStatePending
		st.LastError = "peer not ready"
		return st
	}

	peerNode := h.peerNodeOf(&peer)

	// per-link 互斥锁（同 namespace 内 uid 唯一），避免双方同时建链冲突
	unlock := h.Locks.Lock(localVN.Namespace, link.UID)
	defer unlock()

	if peerNode == h.NodeName {
		// === 同节点：veth pair ===
		return h.handleSameNodeVeth(ctx, &peer, link, localNs, st)
	}
	// === 跨节点：P2P VXLAN ===
	return h.handleCrossNodeVxlan(ctx, &peer, peerNode, link, localNs, st)
}

// handleSameNodeVeth 维持 M2 行为：进对端 netns，建 veth pair。
func (h *SetupHandler) handleSameNodeVeth(
	ctx context.Context,
	peer *vntopov1alpha1.VNode,
	link vntopov1alpha1.LinkSpec,
	localNs string,
	st vntopov1alpha1.LinkStatus,
) vntopov1alpha1.LinkStatus {
	logger := log.FromContext(ctx)

	peerNs, err := h.Netns.LookupPath(ctx, peer.Namespace, peer.Name)
	if err != nil {
		st.State = vntopov1alpha1.LinkStateError
		st.LastError = fmt.Sprintf("lookup peer netns: %v", err)
		return st
	}

	tmpSuffix := strconv.FormatInt(link.UID, 10)
	if err := MakeVethPair(
		localNs, peerNs,
		link.LocalIntf, link.PeerIntf,
		link.LocalIP, link.PeerIP,
		tmpSuffix,
	); err != nil {
		st.State = vntopov1alpha1.LinkStateError
		st.LastError = err.Error()
		return st
	}

	now := nowMetav1()
	st.Mode = vntopov1alpha1.LinkModeVeth
	st.State = vntopov1alpha1.LinkStateEstablished
	st.LastError = ""
	st.EstablishedAt = &now
	logger.Info("veth link established",
		"local_intf", link.LocalIntf, "peer_intf", link.PeerIntf)
	return st
}

// handleCrossNodeVxlan 在本端 pod netns 起一个 P2P VXLAN 设备指向对端节点。
//
// 操作只在本节点完成（VXLAN 是双向独立、对称的）：
//
//   - VNI = ComputeVNI(namespace, uid) → 双方算出同一个值
//   - Local = 本节点 underlay IP（启动时探测）
//   - Remote = 对端节点 InternalIP（K8s API 查 Node 拿）
//   - dev = 本节点 underlay 网卡 ifindex
//
// 对端 agent 看到对端 VNode 后会做对称的事，用同一个 VNI 建出指向我方的
// 设备 → 通信成立。
func (h *SetupHandler) handleCrossNodeVxlan(
	ctx context.Context,
	peer *vntopov1alpha1.VNode,
	peerNode string,
	link vntopov1alpha1.LinkSpec,
	localNs string,
	st vntopov1alpha1.LinkStatus,
) vntopov1alpha1.LinkStatus {
	logger := log.FromContext(ctx).WithValues("peer_node", peerNode)

	st.Mode = vntopov1alpha1.LinkModeVXLAN

	if peerNode == "" {
		st.State = vntopov1alpha1.LinkStatePending
		st.LastError = "peer node unknown (status.hostNode/nodeSelector both empty)"
		return st
	}

	if h.Underlay == nil {
		st.State = vntopov1alpha1.LinkStateError
		st.LastError = "agent underlay not initialized"
		return st
	}

	// 解析对端节点 underlay IP
	peerIP, err := h.NodeIP.LookupNodeIP(ctx, peerNode)
	if err != nil {
		st.State = vntopov1alpha1.LinkStatePending
		st.LastError = fmt.Sprintf("lookup peer node ip: %v", err)
		return st
	}
	peerIPParsed := net.ParseIP(peerIP)
	if peerIPParsed == nil {
		st.State = vntopov1alpha1.LinkStateError
		st.LastError = fmt.Sprintf("peer node ip %q invalid", peerIP)
		return st
	}

	vni := common.ComputeVNI(peer.Namespace, link.UID)

	if err := MakeVxlanLink(VxlanParams{
		VNI:         vni,
		Local:       h.Underlay.LocalIP,
		Remote:      peerIPParsed,
		UnderlayIdx: h.Underlay.IfaceIdx,
		UnderlayMTU: h.Underlay.MTU,
		DstPort:     common.VXLANDefaultPort,
		PodNsPath:   localNs,
		IntfName:    link.LocalIntf,
		CIDR:        link.LocalIP,
	}); err != nil {
		st.State = vntopov1alpha1.LinkStateError
		st.LastError = err.Error()
		st.VNI = vni
		st.UnderlayIP = peerIP
		return st
	}

	now := nowMetav1()
	st.State = vntopov1alpha1.LinkStateEstablished
	st.LastError = ""
	st.VNI = vni
	st.UnderlayIP = peerIP
	st.EstablishedAt = &now
	logger.Info("vxlan link established",
		"local_intf", link.LocalIntf, "vni", vni, "remote", peerIP)
	return st
}
