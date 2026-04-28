/*
Copyright 2026 BUPT AIOps Lab.
*/

package agent

import (
	"context"

	vntopov1alpha1 "github.com/bupt-aiops/vntopo-operator/api/v1alpha1"
)

// MakeVXLAN 在 host 上建立"pod 端 veth + host 端 veth + vxlan + bridge"，
// 把跨节点的两端逻辑直连。
//
// 详细命令序列见 docs/architecture.md §4.2。
//
// 关键参数：
//   - vni        由 controller 分配并写入 status.linkStatus[uid].vni
//   - underlayIP 对端节点 InternalIP
//   - localIP    本节点 InternalIP（用于 vxlan local 字段）
//   - mtu        underlay MTU - VXLANMTUOverhead
//
// 幂等性：使用 netlink Add() 时若设备已存在应忽略 -EEXIST；
// bridge 加入 master 时若已是该 master 也跳过。
func MakeVXLAN(
	ctx context.Context,
	local *vntopov1alpha1.VNode,
	link vntopov1alpha1.LinkSpec,
	vni uint32,
	localUnderlayIP, peerUnderlayIP string,
	underlayIface string,
	mtu int,
) error {
	// TODO(M2): netlink-based 实现
	return nil
}

// DeleteVXLAN 清理本节点上跨节点链路的设备：vh<uid> / vx<uid> / br<uid> / pod 内 intf。
func DeleteVXLAN(
	ctx context.Context,
	local *vntopov1alpha1.VNode,
	link vntopov1alpha1.LinkSpec,
) error {
	// TODO(M2)
	return nil
}
