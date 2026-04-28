/*
Copyright 2026 BUPT AIOps Lab.
*/

package common

import (
	"fmt"
	"regexp"
)

// Linux 接口名上限是 IFNAMSIZ-1 = 15。
const MaxIntfNameLen = 15

// 简化版接口名校验：[a-zA-Z0-9._-]，首字符为字母/数字。
var intfNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,14}$`)

// ValidateIntfName 校验 pod 内接口名是否合法。
func ValidateIntfName(name string) error {
	if !intfNameRe.MatchString(name) {
		return fmt.Errorf("invalid interface name %q (must match %s, len<=15)", name, intfNameRe.String())
	}
	return nil
}

// HostVethName 跨节点链路在宿主机侧的 veth 名（与 vxlan/bridge 配对）。
//
//	uid=42 -> "vh42"
func HostVethName(uid int64) string {
	return fmt.Sprintf("vh%d", uid)
}

// VXLANDevName 跨节点链路的 VXLAN 设备名。
//
//	uid=42 -> "vx42"
func VXLANDevName(uid int64) string {
	return fmt.Sprintf("vx%d", uid)
}

// BridgeDevName 跨节点链路的桥设备名（联通 host-side veth + vxlan）。
//
//	uid=42 -> "br42"
func BridgeDevName(uid int64) string {
	return fmt.Sprintf("br%d", uid)
}
