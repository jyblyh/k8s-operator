/*
Copyright 2026 BUPT AIOps Lab.
*/

// Package roleinjector 把 VNode.spec.role + spec.roleConfig + spec.links
// 翻译成具体的 Pod 启动命令、ConfigMap 数据、安全上下文等。
//
// 设计原则
// ========
//
//  1. **Pure function**：Inject 不调用 K8s API，只接收 VNode、返回 RoleInject；
//     方便单元测试、golden file 比对。
//
//  2. **每个 role 一个文件**：host.go / router.go / switch.go / firewall.go /
//     dhcp.go / dns.go / ws.go。把 role-specific 知识本地化，加新 role 不会
//     冲击其它文件。
//
//  3. **用户优先级**：用户在 spec.template.containers[0].command 已自填命令时，
//     注入器返回的 Command 不会覆盖（renderPod 端做 merge）。这给高级用户保留
//     escape hatch；roleConfig 走默认时仍然有合理行为。
//
//  4. **ConfigMap hash**：注入器输出的所有 ConfigMap.Data 在 controller 端被
//     哈希成 status.configHash 写到 VNode.status；agent 监听到变化时调
//     ReloadCommand 执行热更新。
//
// 使用方
// ======
//
//	injector := roleinjector.For(vn.Spec.Role)
//	inject, err := injector.Inject(&vn)
//	// inject.ConfigMaps 让 controller 去 ensure
//	// inject.Command/Mounts/Volumes/Privileged 让 renderPod 注入主容器
//	// inject.ReloadCommand 写到 ConfigMap reload 时由 agent exec
package roleinjector

import (
	corev1 "k8s.io/api/core/v1"

	vntopov1alpha1 "github.com/jyblyh/k8s-operator/api/v1alpha1"
)

// RoleInjector 是 role-specific 的渲染器接口。
type RoleInjector interface {
	// Inject 根据 vn 计算需要注入到 pod 的所有 role-specific 资源。
	// 必须是 pure function：不调 K8s API、不读文件。
	Inject(vn *vntopov1alpha1.VNode) (*RoleInject, error)
}

// RoleInject 是注入器的产出，由 controller 消费。
type RoleInject struct {
	// Command 主容器启动命令；用户在 template 里已填则不覆盖。
	Command []string

	// Args 主容器 args；一般为空（command 已经是完整 shell 脚本）。
	Args []string

	// Env 追加到主容器的环境变量。
	Env []corev1.EnvVar

	// Mounts 追加到主容器的 volumeMounts。
	Mounts []corev1.VolumeMount

	// Volumes 追加到 pod 的 volumes。
	Volumes []corev1.Volume

	// ConfigMaps 是 controller 需要 create / update / 加 OwnerReference 的 ConfigMap
	// 列表。.Namespace 由 controller 设为 VNode.Namespace；用户也可以预填。
	ConfigMaps []corev1.ConfigMap

	// Privileged 是否给主容器开 privileged。
	Privileged bool

	// Capabilities 额外 capability，例如 [NET_ADMIN, NET_RAW]。
	Capabilities []corev1.Capability

	// ReloadCommand 当 ConfigMap data 内容变化时（status.configHash 变化），
	// agent 通过 docker exec 主容器执行的命令。
	//
	// 例：
	//   router/fw → ["/bin/sh","-c","/usr/lib/frr/frrinit.sh restart"]
	//   dhcp      → ["/bin/sh","-c","kill -HUP $(pidof dhcpd) || true"]
	//   sw        → 空（OVS 端口动态加，无需重启）
	//
	// 空切片表示该 role 没有 reload 动作；agent 会把 status.serviceReload.state
	// 写为 NotApplicable，不做 exec。
	ReloadCommand []string
}

// For 按 role 取注入器；未知 role 返回 NoopInjector（不做任何注入，仅保留
// 用户在 template 里手填的内容）。
func For(role vntopov1alpha1.VNodeRole) RoleInjector {
	switch role {
	case vntopov1alpha1.RoleHost:
		return &hostInjector{}
	case vntopov1alpha1.RoleR:
		return &routerInjector{}
	// 占位：以下 role 在后续 Phase 实装。当前阶段返回 noop 让用户的手填
	// template 仍然能跑（行为退化为 M3 写法）。
	case vntopov1alpha1.RoleSW, vntopov1alpha1.RoleASW, vntopov1alpha1.RoleCSW:
		return &noopInjector{}
	case vntopov1alpha1.RoleFW:
		return &noopInjector{}
	case vntopov1alpha1.RoleDHCP:
		return &noopInjector{}
	case vntopov1alpha1.RoleDNS:
		return &noopInjector{}
	case vntopov1alpha1.RoleWS:
		return &noopInjector{}
	default:
		return &noopInjector{}
	}
}

// noopInjector 用于尚未实装或未知 role：返回空 RoleInject。
type noopInjector struct{}

func (noopInjector) Inject(_ *vntopov1alpha1.VNode) (*RoleInject, error) {
	return &RoleInject{}, nil
}
