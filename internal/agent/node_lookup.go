/*
Copyright 2026 BUPT AIOps Lab.
*/

package agent

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NodeIPResolver 把 nodeName 翻译成 InternalIP。
//
// VXLAN 的 remote 字段需要的是对端节点的 underlay IP，
// 而我们手头只有对端 VNode 的 status.hostNode 或 spec.nodeSelector
// （都只是节点名），所以需要走 K8s API：
//
//	Node.Status.Addresses 中 type=InternalIP 那条
//
// Reader 推荐传 `mgr.GetAPIReader()`（直连 apiserver），原因：
//   - 节点 IP 几乎不变，没必要为它额外起一份全集群 Node Informer
//   - 避免 controller-runtime 因为我们查 Node 而把 Node 类型注册进 cache
//     从而开销额外内存 + 强制要求 nodes list/watch 大权限
//   - 单次 Get 调用 apiserver 在 ~10ms 量级，跨节点建链不是热点路径
type NodeIPResolver struct {
	Reader client.Reader
}

// LookupNodeIP 取节点的 InternalIP（IPv4）。
func (n *NodeIPResolver) LookupNodeIP(ctx context.Context, nodeName string) (string, error) {
	if nodeName == "" {
		return "", fmt.Errorf("empty node name")
	}
	var node corev1.Node
	if err := n.Reader.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
		return "", fmt.Errorf("get node %s: %w", nodeName, err)
	}
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP && addr.Address != "" {
			return addr.Address, nil
		}
	}
	// 退而求其次，找 ExternalIP（裸金属集群常见）
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeExternalIP && addr.Address != "" {
			return addr.Address, nil
		}
	}
	return "", fmt.Errorf("node %s has no InternalIP / ExternalIP", nodeName)
}
