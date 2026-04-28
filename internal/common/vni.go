/*
Copyright 2026 BUPT AIOps Lab.
*/

package common

import (
	"hash/fnv"
	"strconv"
)

// VNI 24-bit 空间。
const VNIMask uint32 = 0x00FFFFFF

// ComputeVNI 给 (namespace, uid) 生成一个 24-bit VNI。
//
// 这是 controller 用来"提议"VNI 的纯函数；如果落到 status 时发现冲突，
// controller 会线性 +1 探测直到无冲突，最终结果写入 status.linkStatus[uid].vni。
func ComputeVNI(namespace string, uid int64) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(namespace))
	_, _ = h.Write([]byte(":"))
	_, _ = h.Write([]byte(strconv.FormatInt(uid, 10)))
	return h.Sum32() & VNIMask
}

// NextVNI 在哈希冲突时线性步进，跳过 0 (保留)。
func NextVNI(v uint32) uint32 {
	v = (v + 1) & VNIMask
	if v == 0 {
		v = 1
	}
	return v
}
