/*
Copyright 2026 BUPT AIOps Lab.
*/

package agent

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

// MakeVethPair 在两端 Pod 的 netns 之间建立一对 veth 并配置 IP。
//
// 参数
//
//   - localNsPath / peerNsPath：本端 / 对端 netns 文件路径，形如 /proc/<pid>/ns/net
//   - localIntf  / peerIntf  ：本端 / 对端在各自 netns 内最终的接口名
//   - localCIDR  / peerCIDR  ：形如 "10.0.0.1/24"；为空则不配 IP
//   - tmpSuffix             ：用于在 host netns 创建 veth 时的临时名后缀，
//     建议传 link.UID 字符串，确保同节点上多条 link 并发建链不会临时名相撞
//
// 幂等性
//
//   - 如果两端目标接口名都已存在，函数直接返回 nil（不重复建）
//   - 如果只有一端存在（说明上次没建完），先清理掉残留再重建
//
// 实现要点
//
//  1. **LockOSThread**：netns 是线程级状态。Go 的 goroutine 会被调度到任意
//     OS thread，所以一旦我们 setns 切换了，必须 lock 住当前 thread，否则
//     goroutine 在中途被调度到别的 thread 上，ns 又跳回宿主 ns 了。
//
//  2. **defer 还原 netns**：函数返回前一定要切回 origNs，否则 worker 复用此
//     thread 处理下一任务时，会以为自己还在某个 Pod 的 netns 里，错乱。
//
//  3. **临时名 + push + 在目标 ns 内 rename**：因为两端最终接口名可能相同
//     （比如对称命名），如果在 host netns 直接建成 final 名，两端同名设备
//     会冲突。先用 tmpA / tmpB 临时名建好 pair，push 到目标 ns 后再 rename。
func MakeVethPair(
	localNsPath, peerNsPath string,
	localIntf, peerIntf string,
	localCIDR, peerCIDR string,
	tmpSuffix string,
) error {
	if localNsPath == "" || peerNsPath == "" {
		return fmt.Errorf("netns path empty")
	}
	if localIntf == "" || peerIntf == "" {
		return fmt.Errorf("interface name empty")
	}

	// 锁住 OS thread——下面所有 SetNs 都依赖它。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	origNs, err := netns.Get()
	if err != nil {
		return fmt.Errorf("get current netns: %w", err)
	}
	defer origNs.Close()
	defer func() { _ = netns.Set(origNs) }() // 函数退出前还原宿主 ns

	nsLocal, err := netns.GetFromPath(localNsPath)
	if err != nil {
		return fmt.Errorf("open local netns %s: %w", localNsPath, err)
	}
	defer nsLocal.Close()

	nsPeer, err := netns.GetFromPath(peerNsPath)
	if err != nil {
		return fmt.Errorf("open peer netns %s: %w", peerNsPath, err)
	}
	defer nsPeer.Close()

	// 幂等检查
	localExists, err := linkExistsInNs(origNs, nsLocal, localIntf)
	if err != nil {
		return fmt.Errorf("check local link: %w", err)
	}
	peerExists, err := linkExistsInNs(origNs, nsPeer, peerIntf)
	if err != nil {
		return fmt.Errorf("check peer link: %w", err)
	}
	if localExists && peerExists {
		return nil
	}
	// 单端残留 → 清理再建（更激进但更稳妥）
	if localExists {
		if err := deleteLinkInNs(origNs, nsLocal, localIntf); err != nil {
			return fmt.Errorf("cleanup stale local link: %w", err)
		}
	}
	if peerExists {
		if err := deleteLinkInNs(origNs, nsPeer, peerIntf); err != nil {
			return fmt.Errorf("cleanup stale peer link: %w", err)
		}
	}

	// 必须在宿主 ns 才能 LinkAdd 一对 veth 并控制其 ns 归属。
	if err := netns.Set(origNs); err != nil {
		return fmt.Errorf("setns(origNs) before LinkAdd: %w", err)
	}

	// Linux 接口名最长 15 字符——临时名留好余量。
	tmpA := truncate("vta"+tmpSuffix, 15)
	tmpB := truncate("vtb"+tmpSuffix, 15)

	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: tmpA},
		PeerName:  tmpB,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		return fmt.Errorf("LinkAdd veth pair: %w", err)
	}

	// 拿两端 link 引用
	linkA, err := netlink.LinkByName(tmpA)
	if err != nil {
		return fmt.Errorf("LinkByName %s: %w", tmpA, err)
	}
	linkB, err := netlink.LinkByName(tmpB)
	if err != nil {
		_ = netlink.LinkDel(linkA) // 回滚
		return fmt.Errorf("LinkByName %s: %w", tmpB, err)
	}

	// push 到目标 netns
	if err := netlink.LinkSetNsFd(linkA, int(nsLocal)); err != nil {
		_ = netlink.LinkDel(linkA)
		return fmt.Errorf("push %s -> local ns: %w", tmpA, err)
	}
	if err := netlink.LinkSetNsFd(linkB, int(nsPeer)); err != nil {
		// linkA 已 push 走了，回滚得切到 nsLocal 删掉
		_ = deleteLinkInNs(origNs, nsLocal, tmpA)
		return fmt.Errorf("push %s -> peer ns: %w", tmpB, err)
	}

	// 在两端 netns 内分别 rename + up + 配 IP
	if err := configureInNs(origNs, nsLocal, tmpA, localIntf, localCIDR); err != nil {
		// 出错时尽量回滚两端
		_ = deleteLinkInNs(origNs, nsLocal, tmpA)
		_ = deleteLinkInNs(origNs, nsPeer, tmpB)
		return fmt.Errorf("configure local end: %w", err)
	}
	if err := configureInNs(origNs, nsPeer, tmpB, peerIntf, peerCIDR); err != nil {
		// local 端已配好，但 peer 失败，整对都删掉
		_ = deleteLinkInNs(origNs, nsLocal, localIntf)
		_ = deleteLinkInNs(origNs, nsPeer, tmpB)
		return fmt.Errorf("configure peer end: %w", err)
	}

	return nil
}

// DeleteVethEnd 在指定 netns 内删掉单个接口。删一端，对端 veth 内核会自动删。
func DeleteVethEnd(nsPath, intf string) error {
	if nsPath == "" || intf == "" {
		return fmt.Errorf("nsPath / intf empty")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	origNs, err := netns.Get()
	if err != nil {
		return fmt.Errorf("get current netns: %w", err)
	}
	defer origNs.Close()
	defer func() { _ = netns.Set(origNs) }()

	nsh, err := netns.GetFromPath(nsPath)
	if err != nil {
		return fmt.Errorf("open ns %s: %w", nsPath, err)
	}
	defer nsh.Close()

	return deleteLinkInNs(origNs, nsh, intf)
}

// configureInNs 在 ns 内 rename oldName -> newName，up，配 CIDR（可选）。
// 调用前外部已 LockOSThread。
func configureInNs(origNs, ns netns.NsHandle, oldName, newName, cidr string) error {
	if err := netns.Set(ns); err != nil {
		return fmt.Errorf("setns: %w", err)
	}
	defer func() { _ = netns.Set(origNs) }() // 函数返回前切回

	link, err := netlink.LinkByName(oldName)
	if err != nil {
		return fmt.Errorf("find link %s: %w", oldName, err)
	}
	if oldName != newName {
		if err := netlink.LinkSetName(link, newName); err != nil {
			return fmt.Errorf("rename %s -> %s: %w", oldName, newName, err)
		}
		// rename 后 link.Attrs().Name 变了，重新 LinkByName 取新引用
		link, err = netlink.LinkByName(newName)
		if err != nil {
			return fmt.Errorf("re-find link after rename: %w", err)
		}
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("set up: %w", err)
	}
	if cidr != "" {
		addr, err := netlink.ParseAddr(cidr)
		if err != nil {
			return fmt.Errorf("parse cidr %s: %w", cidr, err)
		}
		if err := netlink.AddrAdd(link, addr); err != nil {
			// AddrAdd 对已存在地址会返回 EEXIST；视作幂等成功
			if !errors.Is(err, syscall.EEXIST) {
				return fmt.Errorf("add addr %s: %w", cidr, err)
			}
		}
	}
	return nil
}

// linkExistsInNs 在 ns 中查接口是否存在。调用前需 LockOSThread。
func linkExistsInNs(origNs, ns netns.NsHandle, name string) (bool, error) {
	if err := netns.Set(ns); err != nil {
		return false, fmt.Errorf("setns: %w", err)
	}
	defer func() { _ = netns.Set(origNs) }()

	_, err := netlink.LinkByName(name)
	if err == nil {
		return true, nil
	}
	if _, ok := err.(netlink.LinkNotFoundError); ok {
		return false, nil
	}
	// 老版本 netlink 用 unix.ENODEV 包装；做下兼容
	if errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.ENOENT) {
		return false, nil
	}
	return false, err
}

// deleteLinkInNs 在 ns 中按名字删一个 link；不存在视作成功。
func deleteLinkInNs(origNs, ns netns.NsHandle, name string) error {
	if err := netns.Set(ns); err != nil {
		return fmt.Errorf("setns: %w", err)
	}
	defer func() { _ = netns.Set(origNs) }()

	link, err := netlink.LinkByName(name)
	if err != nil {
		if _, ok := err.(netlink.LinkNotFoundError); ok {
			return nil
		}
		if errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.ENOENT) {
			return nil
		}
		return fmt.Errorf("find link %s: %w", name, err)
	}
	return netlink.LinkDel(link)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
