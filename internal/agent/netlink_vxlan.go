/*
Copyright 2026 BUPT AIOps Lab.
*/

package agent

import (
	"errors"
	"fmt"
	"net"
	"runtime"
	"syscall"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"

	"github.com/jyblyh/k8s-operator/internal/common"
)

// VxlanParams 描述一条跨节点 link 在本端要建立的 VXLAN 接口参数。
//
// 双方各调一次 MakeVxlanLink，只关心自己这一端：
//
//	本端 namespace=demo, uid=1, host1@nodeA → 对端 host2@nodeB
//	  → 本节点 nodeA 上：
//	      VxlanParams{ VNI=fnv32(demo:1), Local=nodeA_IP, Remote=nodeB_IP, ... }
//	  → 创建一个 vxlan 设备并 push 到 host1 pod netns，rename 成 LocalIntf
//
//	对端 nodeB 上的 agent 看到对端 VNode 后做对称的事，VNI 相同 → 通信成立。
type VxlanParams struct {
	// VNI = ComputeVNI(namespace, uid)，双向一致
	VNI uint32

	// 本节点 underlay IP（vxlan 包源 IP）
	Local net.IP
	// 对端节点 underlay IP（vxlan 包目的 IP）
	Remote net.IP
	// underlay 网卡 ifindex
	UnderlayIdx int
	// underlay 网卡 MTU；vxlan 设备 MTU = underlay MTU - VXLANMTUOverhead(50)
	UnderlayMTU int
	// VXLAN UDP 端口，一般 4789
	DstPort int

	// 目标 pod netns 路径 /proc/<pid>/ns/net
	PodNsPath string
	// pod ns 内最终的接口名
	IntfName string
	// pod ns 内最终的 IP，CIDR 格式（10.0.0.1/24），可空
	CIDR string
}

// MakeVxlanLink 在本节点创建一个 VXLAN 设备，push 到 pod netns 并配置 IP。
//
// 幂等：pod ns 内同名接口已存在则直接返回 nil（不校验参数是否匹配；
// 后续重建链路通过 DeleteVxlanLink 显式拆除再调本函数）。
//
// 关键实现细节
// ============
//
//   - 我们用 P2P unicast VXLAN：Group 字段填对端 IP，没有组播。
//     这样不依赖 underlay 支持组播，对各种云环境（包括 Calico）都友好。
//
//   - SrcAddr 显式指定。多网卡节点上 kernel 默认会按 ip route get
//     选源，但跨节点跨网卡时可能选错 → 显式 = 不出错。
//
//   - vxlan 设备先在 host ns 建好，然后 LinkSetNsFd 推到 pod ns，
//     再 setns 进 pod ns rename + up + 配 IP。这跟 veth 那边一样。
//
//   - vxlan 设备名我们用 "vx<vni>" + 临时后缀，最长 15 字符限制：
//     "vxlan%d" 会到 11 位（"vxlan16777215"），16 位超长，所以用 "vx%d"。
func MakeVxlanLink(p VxlanParams) error {
	if p.VNI == 0 {
		return fmt.Errorf("vni == 0")
	}
	if p.Local == nil || p.Remote == nil {
		return fmt.Errorf("local/remote ip empty")
	}
	if p.UnderlayIdx == 0 {
		return fmt.Errorf("underlay ifindex == 0")
	}
	if p.PodNsPath == "" || p.IntfName == "" {
		return fmt.Errorf("pod ns path / intf empty")
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	origNs, err := netns.Get()
	if err != nil {
		return fmt.Errorf("get current netns: %w", err)
	}
	defer origNs.Close()
	defer func() { _ = netns.Set(origNs) }()

	podNs, err := netns.GetFromPath(p.PodNsPath)
	if err != nil {
		return fmt.Errorf("open pod netns %s: %w", p.PodNsPath, err)
	}
	defer podNs.Close()

	// 幂等：pod ns 内已有同名接口直接结束。
	exists, err := linkExistsInNs(origNs, podNs, p.IntfName)
	if err != nil {
		return fmt.Errorf("check pod link: %w", err)
	}
	if exists {
		return nil
	}

	// 必须先回到 host ns 才能 LinkAdd vxlan 并附 underlay。
	if err := netns.Set(origNs); err != nil {
		return fmt.Errorf("setns(orig) before LinkAdd: %w", err)
	}

	// 临时名 vx<vni>，最长 15 字符——"vx16777215"=10 字符，安全。
	tmpName := fmt.Sprintf("vx%d", p.VNI)
	if len(tmpName) > 15 {
		tmpName = tmpName[:15]
	}

	// 已经存在同名 vxlan（上次没 push 干净）→ 删了重建，避免 EEXIST。
	if existing, err := netlink.LinkByName(tmpName); err == nil {
		_ = netlink.LinkDel(existing)
	}

	dstPort := p.DstPort
	if dstPort == 0 {
		dstPort = common.VXLANDefaultPort
	}
	mtu := p.UnderlayMTU - common.VXLANMTUOverhead
	if mtu <= 0 {
		mtu = 1450
	}

	vxlan := &netlink.Vxlan{
		LinkAttrs: netlink.LinkAttrs{
			Name: tmpName,
			MTU:  mtu,
		},
		VxlanId:      int(p.VNI),
		VtepDevIndex: p.UnderlayIdx,
		SrcAddr:      p.Local,
		Group:        p.Remote, // unicast 模式下就是 remote
		Port:         dstPort,
		Learning:     false, // 我们是 P2P，不需要内核学 fdb
	}
	if err := netlink.LinkAdd(vxlan); err != nil {
		return fmt.Errorf("LinkAdd vxlan(vni=%d): %w", p.VNI, err)
	}

	link, err := netlink.LinkByName(tmpName)
	if err != nil {
		_ = netlink.LinkDel(vxlan)
		return fmt.Errorf("LinkByName %s after add: %w", tmpName, err)
	}

	// push 到 pod ns
	if err := netlink.LinkSetNsFd(link, int(podNs)); err != nil {
		_ = netlink.LinkDel(link)
		return fmt.Errorf("push vxlan -> pod ns: %w", err)
	}

	// 在 pod ns 内 rename + up + 配 IP（configureInNs 已经处理 EEXIST）
	if err := configureInNs(origNs, podNs, tmpName, p.IntfName, p.CIDR); err != nil {
		// 回滚：从 pod ns 把它删了
		_ = deleteLinkInNs(origNs, podNs, tmpName)
		return fmt.Errorf("configure vxlan in pod ns: %w", err)
	}
	return nil
}

// DeleteVxlanLink 在 pod ns 内删除指定 vxlan 接口。
// 内核会自动连带销毁 vxlan 设备本身，无需在 host ns 再清。
func DeleteVxlanLink(podNsPath, intf string) error {
	if podNsPath == "" || intf == "" {
		return fmt.Errorf("podNsPath / intf empty")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	origNs, err := netns.Get()
	if err != nil {
		return fmt.Errorf("get current netns: %w", err)
	}
	defer origNs.Close()
	defer func() { _ = netns.Set(origNs) }()

	podNs, err := netns.GetFromPath(podNsPath)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return nil
		}
		return fmt.Errorf("open pod netns %s: %w", podNsPath, err)
	}
	defer podNs.Close()

	return deleteLinkInNs(origNs, podNs, intf)
}
