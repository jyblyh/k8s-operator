/*
Copyright 2026 BUPT AIOps Lab.
*/

package common

import (
	"hash/fnv"
	"strconv"
)

// VNI 24-bit 空间。VXLAN VNI = 24-bit；这里我们留下 0 作保留值。
const VNIMask uint32 = 0x00FFFFFF

// ComputeVNI 由 (namespace, uid) **确定性**地算出一条 link 的 VXLAN VNI。
//
// 设计要点
// ========
//
//  1. **确定性**：两端 agent 完全独立、不通信，但只要看到同一对
//     (namespace, uid) 就能算出相同 VNI——这正是跨节点 link 双方
//     起 VTEP 时 VNI 必须一致的根基。controller 不参与分配，
//     不写 status，不存任何 vni 表。
//
//  2. **24-bit 空间**：VXLAN 头部 VNI 字段就是 24-bit。冲突概率非常低
//     （N=10000 link 时 ~3% 的生日悖论冲突；M4 加 webhook 校验时再处理）。
//
//  3. **保留 0**：VNI=0 在某些实现里有特殊语义，统一跳过。
//
//  4. **跨 namespace 隔离**：把 namespace 字符串掺进哈希里，避免不同
//     namespace 同 uid 的两条 link 撞同 VNI（哪怕它们落到同一节点上）。
func ComputeVNI(namespace string, uid int64) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(namespace))
	_, _ = h.Write([]byte(":"))
	_, _ = h.Write([]byte(strconv.FormatInt(uid, 10)))
	v := h.Sum32() & VNIMask
	if v == 0 {
		v = 1
	}
	return v
}
