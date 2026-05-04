/*
Copyright 2026 BUPT AIOps Lab.
*/

package agent

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

// Underlay 描述本节点 VXLAN 隧道封装所走的 underlay 网络属性。
//
// VXLAN 内部其实是 "把以太帧塞进 UDP 包，从某张物理/虚拟网卡发到对端节点"。
// 所以一个 VXLAN 设备在创建时必须告诉内核：
//
//   - 走哪张网卡发出去（VtepDevIndex / dev）
//   - 用哪个 IP 当源（local）
//   - MTU 该多大（underlay MTU - 50 字节 VXLAN+UDP+IP 开销）
//
// 这些信息一台节点上对所有跨节点 link 都是公用的，只需要在 agent 启动时
// 探测一次然后缓存。
type Underlay struct {
	IfaceName string // 网卡名，例如 "eth0"
	IfaceIdx  int    // netlink ifindex
	LocalIP   net.IP // 这块网卡上配的主 IPv4，写到 vxlan.SrcAddr
	MTU       int    // 这块网卡的 MTU
}

// String 用于日志打印。
func (u *Underlay) String() string {
	if u == nil {
		return "<nil>"
	}
	return fmt.Sprintf("iface=%s idx=%d local=%s mtu=%d", u.IfaceName, u.IfaceIdx, u.LocalIP, u.MTU)
}

// DetectUnderlay 自动发现 underlay 网卡。优先级：
//
//  1. 显式传入 ifaceHint（命令行 --underlay-iface）→ 直接用
//  2. 显式传入 nodeIP（NODE_IP 环境变量，由 DaemonSet status.hostIP 注入）
//     → 找哪张网卡上配了这个 IP，用那张
//  3. 兜底：默认路由（dst 0.0.0.0/0）的 dev → 用那张
//
// 返回的 Underlay.LocalIP 永远等于"探测出来这张网卡上的第一个非 link-local
// IPv4"，这样 VXLAN 双向选 src 时一致——对端节点根据 NodeStatus.Addresses
// 拿到我们这边 InternalIP，跟 LocalIP 应该一致；如果 K8s 注册的 InternalIP
// 跟 underlay 网卡 IP 不一致，请通过 --underlay-iface 显式指定。
func DetectUnderlay(ifaceHint string, nodeIP string) (*Underlay, error) {
	if ifaceHint != "" {
		return underlayFromIface(ifaceHint)
	}
	if nodeIP != "" {
		if u, err := underlayFromIP(nodeIP); err == nil {
			return u, nil
		}
		// fallback to default route
	}
	return underlayFromDefaultRoute()
}

func underlayFromIface(name string) (*Underlay, error) {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return nil, fmt.Errorf("LinkByName %s: %w", name, err)
	}
	return underlayFromLink(link)
}

func underlayFromIP(ipStr string) (*Underlay, error) {
	target := net.ParseIP(ipStr)
	if target == nil {
		return nil, fmt.Errorf("parse ip %q: invalid", ipStr)
	}
	target = target.To4()
	if target == nil {
		return nil, fmt.Errorf("only IPv4 underlay supported, got %s", ipStr)
	}
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("LinkList: %w", err)
	}
	for _, link := range links {
		addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if a.IP.Equal(target) {
				return underlayFromLink(link)
			}
		}
	}
	return nil, fmt.Errorf("no link has ip %s", ipStr)
}

func underlayFromDefaultRoute() (*Underlay, error) {
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return nil, fmt.Errorf("RouteList v4: %w", err)
	}
	for _, r := range routes {
		// 默认路由：Dst == nil 或 0.0.0.0/0
		if r.Dst == nil || (r.Dst.IP.IsUnspecified() && bitsOnes(r.Dst.Mask) == 0) {
			link, err := netlink.LinkByIndex(r.LinkIndex)
			if err != nil {
				continue
			}
			return underlayFromLink(link)
		}
	}
	return nil, fmt.Errorf("no default route found, please specify --underlay-iface")
}

// underlayFromLink 从一张网卡 link 中抽出 Underlay 配置。
func underlayFromLink(link netlink.Link) (*Underlay, error) {
	attrs := link.Attrs()
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return nil, fmt.Errorf("AddrList %s: %w", attrs.Name, err)
	}
	var localIP net.IP
	for _, a := range addrs {
		// 跳过 link-local 169.254.x.x
		if a.IP.IsLinkLocalUnicast() {
			continue
		}
		localIP = a.IP
		break
	}
	if localIP == nil {
		return nil, fmt.Errorf("link %s has no usable ipv4", attrs.Name)
	}
	return &Underlay{
		IfaceName: attrs.Name,
		IfaceIdx:  attrs.Index,
		LocalIP:   localIP,
		MTU:       attrs.MTU,
	}, nil
}

// bitsOnes 返回 mask 中置 1 的 bit 数。0.0.0.0/0 时返回 0。
func bitsOnes(mask net.IPMask) int {
	ones, _ := mask.Size()
	return ones
}
