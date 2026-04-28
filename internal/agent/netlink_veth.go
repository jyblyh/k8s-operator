/*
Copyright 2026 BUPT AIOps Lab.
*/

package agent

import (
	"context"

	vntopov1alpha1 "github.com/jyblyh/k8s-operator/api/v1alpha1"
)

// MakeVeth 在两个同节点 Pod 的 netns 之间建立一对 veth。
//
// 实现策略（M1）：直接复用 p2pnet 现有 koko 调用。
//
//	import "github.com/redhat-nfvpe/koko/api"
//	veth1 := koko.VEth{NsName: netnsA, LinkName: link.LocalIntf, IPAddr: ...}
//	veth2 := koko.VEth{NsName: netnsB, LinkName: link.PeerIntf,  IPAddr: ...}
//	koko.MakeVeth(veth1, veth2)
//
// 函数应当是幂等的：若设备已存在并且配置一致，直接返回 nil。
func MakeVeth(ctx context.Context, local, peer *vntopov1alpha1.VNode, link vntopov1alpha1.LinkSpec) error {
	// TODO(M1)
	return nil
}

// DeleteVeth 删除同节点 veth 对（删一端，另一端自动删）。
func DeleteVeth(ctx context.Context, local *vntopov1alpha1.VNode, link vntopov1alpha1.LinkSpec) error {
	// TODO(M1)
	return nil
}
