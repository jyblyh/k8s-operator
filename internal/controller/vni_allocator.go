/*
Copyright 2026 BUPT AIOps Lab.
*/

package controller

import (
	"context"

	vntopov1alpha1 "github.com/bupt-aiops/vntopo-operator/api/v1alpha1"
	"github.com/bupt-aiops/vntopo-operator/internal/common"
)

// AllocateVNI 给 (namespace, uid) 分配一个 24-bit VNI，保证同 namespace 内不冲突。
//
// 策略：
//  1. 先用 ComputeVNI 哈希出一个候选；
//  2. 列出当前 namespace 下所有 VNode 的 status.linkStatus[].vni，构成已用集合；
//  3. 候选若已被使用，线性 +1 探测直到无冲突；
//  4. 返回最终 VNI。
//
// M2 中实装；M0 留接口。
func AllocateVNI(ctx context.Context, namespace string, uid int64, used map[uint32]struct{}) uint32 {
	v := common.ComputeVNI(namespace, uid)
	for {
		if _, ok := used[v]; !ok {
			return v
		}
		v = common.NextVNI(v)
	}
}

// CollectUsedVNIs 从 namespace 下所有 VNode 的 status 中收集已分配 VNI。
//
// M2 中实装。
func CollectUsedVNIs(ctx context.Context, list *vntopov1alpha1.VNodeList) map[uint32]struct{} {
	used := map[uint32]struct{}{}
	for i := range list.Items {
		for _, ls := range list.Items[i].Status.LinkStatus {
			if ls.VNI != 0 {
				used[ls.VNI] = struct{}{}
			}
		}
	}
	return used
}
