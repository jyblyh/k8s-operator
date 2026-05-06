/*
Copyright 2026 BUPT AIOps Lab.
*/

package roleinjector

import (
	"fmt"
	"strings"

	vntopov1alpha1 "github.com/jyblyh/k8s-operator/api/v1alpha1"
)

// hostInjector 处理 role=host 的 Pod。
//
// host 节点的特点：
//   - 不跑路由协议、不当网桥；只是端节点
//   - 唯一可能的"role-specific"动作：等接口建好 → 设默认网关 → 加静态路由 → sleep
//   - 一般不需要 ConfigMap（除非用户用 ExtraConfigMaps）
//   - 不需要 reload（默认网关变化时通过 spec 修改 → Pod 重建即可）
//
// 选择直接在主容器命令里 inline 处理而不是单独 startup script ConfigMap：
//   - 命令很短，不值得多一次 ConfigMap I/O
//   - host 主容器镜像通常带 bash + ip，足以跑 ip route 命令
type hostInjector struct{}

func (hostInjector) Inject(vn *vntopov1alpha1.VNode) (*RoleInject, error) {
	hc := hostConfigOf(vn)

	// 收集"等哪些接口出现"的列表：所有 spec.links 里的 local_intf。
	intfs := make([]string, 0, len(vn.Spec.Links))
	for _, l := range vn.Spec.Links {
		if l.LocalIntf != "" {
			intfs = append(intfs, l.LocalIntf)
		}
	}

	cmd := buildHostCommand(intfs, hc)

	return &RoleInject{
		Command:    cmd,
		Privileged: true, // 容器内执行 ip route 需要 NET_ADMIN；privileged 最简
		// host 不需要 reload（路由变更走 Pod 重建路径）
		ReloadCommand: nil,
	}, nil
}

// hostConfigOf 安全取出 spec.roleConfig.host；不存在时返回零值结构。
func hostConfigOf(vn *vntopov1alpha1.VNode) *vntopov1alpha1.HostConfig {
	if vn == nil || vn.Spec.RoleConfig == nil || vn.Spec.RoleConfig.Host == nil {
		return &vntopov1alpha1.HostConfig{}
	}
	return vn.Spec.RoleConfig.Host
}

// buildHostCommand 拼出主容器 shell 启动脚本。
//
// 逻辑：
//  1. 等所有 link 接口出现（最多 60s，超时也继续，不卡死容器）
//  2. 设默认网关（如果 hc.DefaultGateway 非空）
//  3. 应用静态路由
//  4. 打印当前网络状态便于排障
//  5. sleep infinity
//
// `ip route replace` 是幂等的——已存在的同 dest 路由会被覆盖；不存在则新增。
// 用 replace 而不是 add 避免容器重启后报 EEXIST。
func buildHostCommand(intfs []string, hc *vntopov1alpha1.HostConfig) []string {
	var b strings.Builder

	// shell 选项：-e 任一行失败立即退出；但 ip route 失败不致命，所以脚本里手动
	// 用 `|| true` 保护，整体保留 -e。
	b.WriteString("set -e\n")

	if len(intfs) > 0 {
		b.WriteString("echo '[host-init] waiting for interfaces:'")
		for _, n := range intfs {
			b.WriteString(" " + shellQuote(n))
		}
		b.WriteString("\n")
		b.WriteString("for i in $(seq 1 60); do\n")
		b.WriteString("  ok=1\n")
		for _, n := range intfs {
			b.WriteString(fmt.Sprintf("  ip link show %s >/dev/null 2>&1 || ok=0\n", shellQuote(n)))
		}
		b.WriteString("  if [ \"$ok\" = \"1\" ]; then break; fi\n")
		b.WriteString("  sleep 1\n")
		b.WriteString("done\n")
	}

	if hc.DefaultGateway != "" {
		b.WriteString(fmt.Sprintf("ip route replace default via %s || true\n", shellQuote(hc.DefaultGateway)))
	}
	for _, r := range hc.StaticRoutes {
		if r.Dest == "" || r.Via == "" {
			continue
		}
		b.WriteString(fmt.Sprintf("ip route replace %s via %s || true\n",
			shellQuote(r.Dest), shellQuote(r.Via)))
	}

	b.WriteString("echo '[host-init] network ready:'\n")
	b.WriteString("ip a; ip route\n")
	// PID-1 reaper 尾段，避免 bash 把自己 exec 替换成 sleep（详见 router.go）。
	// host 没有 frr 要 stop，trap 逻辑更简单。
	b.WriteString(hostPid1Tail())

	return []string{"/bin/bash", "-c", b.String()}
}

// hostPid1Tail 是 host 容器尾段——和 router 的 reaper 模式一致，但不用
// 停 frr。bash 阻塞在 wait $BG_PID，SIGCHLD 时自动 reap 孤儿。
func hostPid1Tail() string {
	return strings.Join([]string{
		"echo '[host-init] entering pid-1 reaper loop'",
		"sleep infinity &",
		"BG_PID=$!",
		"trap 'kill $BG_PID 2>/dev/null; exit 0' SIGTERM SIGINT",
		"while kill -0 $BG_PID 2>/dev/null; do",
		"  wait $BG_PID 2>/dev/null || true",
		"done",
	}, "\n") + "\n"
}

// shellQuote 极简 shell 字符串引用：把单引号转义。
//
// 我们只在内部用，输入来自 CRD 字段（K8s API server 已经做过基本校验），
// 不需要完整的 shell escaping 库；万一用户在 IP 里塞了 `;` 类字符，CRD
// 的 string validation 也应该挡住——这里再加一层保险。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
