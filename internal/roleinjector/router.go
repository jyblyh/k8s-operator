/*
Copyright 2026 BUPT AIOps Lab.
*/

package roleinjector

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vntopov1alpha1 "github.com/jyblyh/k8s-operator/api/v1alpha1"
)

// routerInjector 处理 role=r 的 Pod。
//
// 镜像约定：用户填 frr/firewall-v2 类镜像（含 frrinit.sh + vtysh）。
//
// 注入内容：
//   - 主容器命令：开 ip_forward → 启 frr → sleep
//   - ConfigMap "{vnode}-frr"：daemons + frr.conf
//   - VolumeMount /etc/frr (configmap) + /var/run/frr (emptyDir)
//   - SecurityContext.privileged = true（frr 需要 raw socket、netlink）
//   - ReloadCommand = /usr/lib/frr/frrinit.sh restart
//
// 双层幂等：
//   - 不开 OSPF（OspfNetworks 空）时，frr 仍然启动但 OSPF daemon 不跑——纯靠
//     直连转发已足够（Linux 内核自动写直连路由）。
//   - 开 OSPF 时，frr.conf 里写 router-id + network 通告。
type routerInjector struct{}

// 路由器 ConfigMap 内的文件名。
const (
	frrDaemonsKey = "daemons"
	frrConfKey    = "frr.conf"
	// vtysh.conf 是 FRR 强烈推荐放置的（避免 vtysh 启动警告），即使内容很简单
	vtyshConfKey = "vtysh.conf"
)

func (routerInjector) Inject(vn *vntopov1alpha1.VNode) (*RoleInject, error) {
	rc := routerConfigOf(vn)
	enableFrr := rc.EnableFrr == nil || *rc.EnableFrr // 默认 true
	enableOspf := enableFrr && len(rc.OspfNetworks) > 0

	cmName := vn.Name + "-frr"
	cmData := map[string]string{
		frrDaemonsKey: renderFrrDaemons(enableOspf),
		frrConfKey:    renderFrrConf(vn, rc, enableOspf),
		vtyshConfKey:  fmt.Sprintf("service integrated-vtysh-config\nhostname %s\n", vn.Name),
	}

	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: vn.Namespace,
		},
		Data: cmData,
	}

	cmd := buildRouterCommand(vn, enableFrr)

	mounts := []corev1.VolumeMount{
		{Name: "frr-config", MountPath: "/etc/frr"},
		{Name: "frr-run", MountPath: "/var/run/frr"},
	}
	volumes := []corev1.Volume{
		{
			Name: "frr-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
				},
			},
		},
		{
			Name: "frr-run",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}

	var reload []string
	if enableFrr {
		// frrinit.sh restart 会重新加载 frr.conf（vtysh -f）。
		// 不用 reload 子命令是因为部分 daemon（ospfd）的某些参数变化只有 restart 才生效。
		reload = []string{"/bin/sh", "-c", "/usr/lib/frr/frrinit.sh restart"}
	}

	return &RoleInject{
		Command:       cmd,
		Mounts:        mounts,
		Volumes:       volumes,
		ConfigMaps:    []corev1.ConfigMap{cm},
		Privileged:    true,
		ReloadCommand: reload,
	}, nil
}

// routerConfigOf 安全取出 spec.roleConfig.router；不存在时返回零值。
func routerConfigOf(vn *vntopov1alpha1.VNode) *vntopov1alpha1.RouterConfig {
	if vn == nil || vn.Spec.RoleConfig == nil || vn.Spec.RoleConfig.Router == nil {
		return &vntopov1alpha1.RouterConfig{}
	}
	return vn.Spec.RoleConfig.Router
}

// renderFrrDaemons 生成 /etc/frr/daemons 文件。
//
// 这个文件控制 frrinit.sh 启哪些子 daemon。zebra 是核心（路由表 / netlink 桥接），
// 必须开；ospfd 按是否启用 OSPF 决定。
//
// FRR 8.x 默认 daemons 文件还有十几个 daemon（bgpd/ripd/eigrpd/...），我们都关掉
// 节省内存。
func renderFrrDaemons(enableOspf bool) string {
	on := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	return strings.Join([]string{
		"zebra=yes",
		"bgpd=no",
		"ospfd=" + on(enableOspf),
		"ospf6d=no",
		"ripd=no",
		"ripngd=no",
		"isisd=no",
		"pimd=no",
		"ldpd=no",
		"nhrpd=no",
		"eigrpd=no",
		"babeld=no",
		"sharpd=no",
		"pbrd=no",
		"bfdd=no",
		"fabricd=no",
		"vrrpd=no",
		// 全局选项
		`vtysh_enable=yes`,
		`zebra_options="  -A 127.0.0.1 -s 90000000"`,
		`bgpd_options="   -A 127.0.0.1"`,
		`ospfd_options="  -A 127.0.0.1"`,
		"",
	}, "\n")
}

// renderFrrConf 生成 /etc/frr/frr.conf——FRR 8.x 集成式单文件配置。
//
// 即使不开 OSPF，也要写一个最小骨架，让 vtysh -f 能成功加载。
func renderFrrConf(vn *vntopov1alpha1.VNode, rc *vntopov1alpha1.RouterConfig, enableOspf bool) string {
	var b strings.Builder
	b.WriteString("frr defaults traditional\n")
	b.WriteString(fmt.Sprintf("hostname %s\n", vn.Name))
	b.WriteString("no ipv6 forwarding\n")
	b.WriteString("log file /var/log/frr/frr.log\n")
	b.WriteString("!\n")

	if enableOspf {
		routerID := rc.RouterID
		if routerID == "" {
			routerID = deriveRouterID(vn)
		}
		b.WriteString("router ospf\n")
		if routerID != "" {
			b.WriteString(fmt.Sprintf(" ospf router-id %s\n", routerID))
		}
		for _, n := range rc.OspfNetworks {
			if n == "" {
				continue
			}
			b.WriteString(fmt.Sprintf(" network %s area 0\n", n))
		}
		b.WriteString("!\n")
	}

	b.WriteString("end\n")
	return b.String()
}

// buildRouterCommand 组装主容器启动 shell 脚本。
//
// 必须 echo ip_forward 而不是依赖 frr——zebra 默认不会改 ip_forward sysctl。
func buildRouterCommand(vn *vntopov1alpha1.VNode, enableFrr bool) []string {
	var b strings.Builder
	b.WriteString("set -e\n")
	b.WriteString("echo 1 > /proc/sys/net/ipv4/ip_forward\n")

	if enableFrr {
		// frrinit.sh start 会读 /etc/frr/daemons 启相应 daemon，并加载 frr.conf。
		// 它非守护：返回后子进程已经在跑。
		b.WriteString("echo '[router-init] starting frr...'\n")
		b.WriteString("/usr/lib/frr/frrinit.sh start || true\n")
	} else {
		b.WriteString("echo '[router-init] frr disabled (RoleConfig.Router.EnableFrr=false)'\n")
	}

	// 等所有 link 接口出现，便于 OSPF 及时发现邻居（不等也能跑，OSPF 周期 hello）
	intfs := make([]string, 0, len(vn.Spec.Links))
	for _, l := range vn.Spec.Links {
		if l.LocalIntf != "" {
			intfs = append(intfs, l.LocalIntf)
		}
	}
	if len(intfs) > 0 {
		b.WriteString("echo '[router-init] waiting for interfaces:'")
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

	b.WriteString("echo '[router-init] router ready:'\n")
	b.WriteString("ip a; ip route\n")
	b.WriteString("sleep infinity\n")
	return []string{"/bin/bash", "-c", b.String()}
}

// deriveRouterID 在用户没填 RouterID 时，从 spec.links[0].local_ip 截 host 部分。
//
// OSPF router-id 是 32-bit；用 IPv4 表示即可。优先选第一条 link 的 local_ip
// （去掉 /mask 部分），如果没 link 就返回空（OSPF 用本节点回环 IP 兜底）。
func deriveRouterID(vn *vntopov1alpha1.VNode) string {
	for _, l := range vn.Spec.Links {
		ip := l.LocalIP
		if ip == "" {
			continue
		}
		if i := strings.IndexByte(ip, '/'); i > 0 {
			ip = ip[:i]
		}
		return ip
	}
	return ""
}
