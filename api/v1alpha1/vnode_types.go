/*
Copyright 2026 BUPT AIOps Lab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ============================================================================
//  Spec
// ============================================================================

// VNodeRole 定义了虚拟节点的角色类型，影响调度策略与默认行为。
// +kubebuilder:validation:Enum=host;sw;asw;csw;r;fw;dhcp;dns;ws
type VNodeRole string

const (
	RoleHost VNodeRole = "host"
	RoleSW   VNodeRole = "sw"
	RoleASW  VNodeRole = "asw"
	RoleCSW  VNodeRole = "csw"
	RoleR    VNodeRole = "r"
	RoleFW   VNodeRole = "fw"
	RoleDHCP VNodeRole = "dhcp"
	RoleDNS  VNodeRole = "dns"
	RoleWS   VNodeRole = "ws"
)

// LinkMetrics 描述链路的运行时指标，可由外部探测器（如 ping_exporter）回填。
type LinkMetrics struct {
	// +optional
	BandwidthMbps *float64 `json:"bandwidth_mbps,omitempty"`
	// +optional
	JitterMs *float64 `json:"jitter_ms,omitempty"`
	// +optional
	LatencyMs *float64 `json:"latency_ms,omitempty"`
	// +optional
	LossPercentage *float64 `json:"loss_percentage,omitempty"`
	// +optional
	LastUpdated *metav1.Time `json:"last_updated,omitempty"`
}

// LinkSpec 描述本 VNode 对外的一条点对点链路。两端必须使用相同的 uid。
type LinkSpec struct {
	// 链路在所属 namespace 内的唯一 ID，两端必须用同一个值。
	// +kubebuilder:validation:Minimum=1
	UID int64 `json:"uid"`

	// 对端 VNode 的 name（同 namespace）。
	PeerPod string `json:"peer_pod"`

	// 本端在 pod netns 内的接口名，遵循 Linux 接口命名规则，最大 15 字符。
	// +kubebuilder:validation:MaxLength=15
	LocalIntf string `json:"local_intf"`

	// 对端在 pod netns 内的接口名，遵循 Linux 接口命名规则，最大 15 字符。
	// +kubebuilder:validation:MaxLength=15
	PeerIntf string `json:"peer_intf"`

	// 本端接口 IP（建议 CIDR 格式，例如 "10.0.0.1/24"）。
	// +optional
	LocalIP string `json:"local_ip,omitempty"`

	// 对端接口 IP（信息字段，agent 不会写到对端）。
	// +optional
	PeerIP string `json:"peer_ip,omitempty"`

	// 路由权重 / 链路代价。
	// +optional
	Cost *float64 `json:"cost,omitempty"`

	// +optional
	Metrics *LinkMetrics `json:"metrics,omitempty"`
}

// VNodeSpec 是 VNode 的期望状态。
type VNodeSpec struct {
	// 节点角色。
	Role VNodeRole `json:"role"`

	// 所属数据中心标识。同一 dataCenter 下的所有 VNode 必须共享同一个 nodeSelector
	// （由 webhook 强制校验）。role=r 作为 inter-DC 路由器时可留空。
	// +optional
	DataCenter string `json:"dataCenter,omitempty"`

	// 显式 nodeSelector。同 DC 的非路由器节点必填，且与同 DC 其它 VNode 一致。
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// 高级亲和性配置；可与 nodeSelector 叠加，也可单独使用。
	//
	// 故意用 Schemaless + PreserveUnknownFields：
	//   - 让 controller-gen 不要把 corev1.Affinity 展开成几百行的 OpenAPI schema，
	//     避免 K8s 1.21 的 CRD 校验器解析超大嵌套 schema 时报 unmarshal 错误。
	//   - 这只影响 CRD schema 校验；Go 侧的反序列化仍然按 corev1.Affinity 严格走。
	//
	// +optional
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// Pod 模板。controller 会在创建 Pod 时注入：
	//   · ownerReferences -> VNode
	//   · nodeSelector / affinity（来自 spec.nodeSelector / spec.affinity）
	//   · labels: vntopo.bupt.site/{role,dc,vnode}
	//   · 一个 init container：vntopo-init
	//   · M4 起：按 spec.role 自动注入主容器 command / volumes / mounts /
	//     securityContext，以及关联的 ConfigMap（OSPF / OVS startup / dhcpd.conf 等）。
	//     如果用户在 template.containers[0].command 已自填，则尊重用户值不覆盖。
	//
	// Schemaless 同上，避免展开 PodTemplateSpec 这棵超大 schema 树。
	//
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Template corev1.PodTemplateSpec `json:"template"`

	// 兼容字段，部分组件读取 pod 主 IP 用。
	// +optional
	LocalIP string `json:"localIp,omitempty"`

	// 本 VNode 对外的所有点对点链路。
	// +optional
	Links []LinkSpec `json:"links,omitempty"`

	// RoleConfig 提供 role 特定的运行时参数（OSPF networks / DHCP 子网 / DNS zone 等）。
	// controller 按 spec.role 选取对应子结构来渲染主容器命令和 ConfigMap data。
	//
	// Schemaless：不同 role 字段差别太大，把 schema 一一展开会让 CRD YAML 过百 KB；
	// 实际校验交给 controller 端 + webhook。
	//
	// +optional
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	RoleConfig *RoleConfig `json:"roleConfig,omitempty"`

	// ExtraConfigMaps 是用户附带的 ConfigMap，controller 创建并自动挂载到主容器。
	// 留给高级用户做 escape hatch（角色注入器无法覆盖时手动指定）。
	// +optional
	ExtraConfigMaps []ExtraConfigMap `json:"extraConfigMaps,omitempty"`
}

// ============================================================================
//  RoleConfig：按 role 区分的运行时参数容器
// ============================================================================

// RoleConfig 是不同 role 的参数容器。每次只填一项，controller 根据 spec.role
// 选取对应子结构；填错位置（比如 role=host 但只填了 RoleConfig.Router）controller
// 会把 RoleConfig 当作未提供，走 role 默认值。
type RoleConfig struct {
	// +optional
	Host *HostConfig `json:"host,omitempty"`
	// +optional
	Router *RouterConfig `json:"router,omitempty"`
	// +optional
	Switch *SwitchConfig `json:"switch,omitempty"`
	// +optional
	Firewall *FirewallConfig `json:"firewall,omitempty"`
	// +optional
	DHCP *DHCPConfig `json:"dhcp,omitempty"`
	// +optional
	DNS *DNSConfig `json:"dns,omitempty"`
}

// HostConfig 用于 role=host：默认网关、静态路由。
type HostConfig struct {
	// 默认网关 IP（不带掩码）。空时不设默认路由。
	// +optional
	DefaultGateway string `json:"defaultGateway,omitempty"`

	// 静态路由（可选）。每条会被翻译成 `ip route replace <dest> via <via>`。
	// +optional
	StaticRoutes []StaticRoute `json:"staticRoutes,omitempty"`
}

// StaticRoute 一条静态路由。
type StaticRoute struct {
	Dest string `json:"dest"` // CIDR
	Via  string `json:"via"`  // 下一跳 IP
}

// RouterConfig 用于 role=r：FRR / OSPF。
type RouterConfig struct {
	// OSPF 通告的网段（CIDR）。空切片时 controller 不启 OSPF，仅靠直连转发。
	// +optional
	OspfNetworks []string `json:"ospfNetworks,omitempty"`

	// OSPF router-id（点分十进制）。空时由 controller 用 spec.links[0].local_ip 拼一个。
	// +optional
	RouterID string `json:"routerId,omitempty"`

	// 是否启用 frr 进程。默认 true。把它设为 false 时 controller 只 echo ip_forward
	// 不启 frr，适用于纯静态直连转发场景。
	// +optional
	EnableFrr *bool `json:"enableFrr,omitempty"`
}

// SwitchConfig 用于 role=sw / asw / csw：OVS 桥与 SVI。
type SwitchConfig struct {
	// 二层桥名，默认 "br0"。
	// +optional
	BridgeName string `json:"bridgeName,omitempty"`

	// L3 switch（asw/csw）才用：在 br0 上创建 SVI 接口并配 IP。
	// +optional
	SVIs []SVI `json:"svis,omitempty"`
}

// SVI 在 OVS 桥上的虚拟三层接口。
type SVI struct {
	Name string `json:"name"` // 例如 "vlan10"
	IP   string `json:"ip"`   // CIDR，例如 "10.0.10.1/24"
	// +optional
	VLAN int `json:"vlan,omitempty"`
}

// FirewallConfig 用于 role=fw：本质是 router + iptables。
//
// 不用 inline 嵌入 RouterConfig 是为了避开 controller-gen 对指针嵌入的 deepcopy
// 生成 corner case；直接显式带 OspfNetworks/RouterID/EnableFrr 三个字段。
type FirewallConfig struct {
	// OSPF 通告的网段（CIDR）。空切片时不启 OSPF。
	// +optional
	OspfNetworks []string `json:"ospfNetworks,omitempty"`

	// OSPF router-id。空时由 controller 自动派生。
	// +optional
	RouterID string `json:"routerId,omitempty"`

	// 是否启用 frr 进程。默认 true。
	// +optional
	EnableFrr *bool `json:"enableFrr,omitempty"`

	// 额外的 iptables 规则，启动时按顺序 `iptables` 执行。
	// 例：`-A FORWARD -j ACCEPT`
	// +optional
	IptablesRules []string `json:"iptablesRules,omitempty"`
}

// DHCPConfig 用于 role=dhcp。
type DHCPConfig struct {
	// 监听的接口名（一般是 spec.links[0].local_intf）。
	// +optional
	Interface string `json:"interface,omitempty"`

	// 一组要服务的子网。
	Subnets []DHCPSubnet `json:"subnets,omitempty"`
}

// DHCPSubnet 一个 DHCP 子网定义。
type DHCPSubnet struct {
	Subnet     string   `json:"subnet"`           // CIDR，例如 "10.0.1.0/24"
	RangeStart string   `json:"rangeStart"`       // 池起始 IP
	RangeEnd   string   `json:"rangeEnd"`         // 池结束 IP
	Router     string   `json:"router,omitempty"` // 下发给 client 的默认网关
	DNS        []string `json:"dns,omitempty"`    // 下发给 client 的 DNS server
	// +optional
	LeaseSec int `json:"leaseSec,omitempty"`
}

// DNSConfig 用于 role=dns。
type DNSConfig struct {
	// +optional
	Zones []DNSZone `json:"zones,omitempty"`
}

// DNSZone 一个 DNS zone。
type DNSZone struct {
	Domain  string      `json:"domain"`            // 例 "example.local"
	Records []DNSRecord `json:"records,omitempty"` // A / NS / CNAME
}

// DNSRecord 一条 DNS 记录。
type DNSRecord struct {
	Name  string `json:"name"`            // host1
	Type  string `json:"type"`            // A / CNAME / NS
	Value string `json:"value"`           // 值
	TTL   int    `json:"ttl,omitempty"`   // 默认 300
	Class string `json:"class,omitempty"` // 默认 IN
}

// ExtraConfigMap 描述一个用户附带的 ConfigMap 资源，controller 会以 OwnerReference
// 形式 create/update 它，并自动挂载到主容器指定路径。
type ExtraConfigMap struct {
	// ConfigMap 名（同 namespace）。空时 controller 自动用 "{vnode-name}-extra-{idx}"。
	// +optional
	Name string `json:"name,omitempty"`

	// ConfigMap data：键 = 文件名，值 = 文件内容。
	Data map[string]string `json:"data"`

	// 主容器挂载点，例如 "/etc/myapp"。
	MountPath string `json:"mountPath"`

	// 是否只读（默认 false）。
	// +optional
	ReadOnly bool `json:"readOnly,omitempty"`
}

// ============================================================================
//  Status
// ============================================================================

// VNodePhase 表示 VNode 整体生命周期阶段。
// +kubebuilder:validation:Enum=Pending;Creating;Ready;Degraded;Deleting;Failed
type VNodePhase string

const (
	PhasePending  VNodePhase = "Pending"
	PhaseCreating VNodePhase = "Creating"
	PhaseReady    VNodePhase = "Ready"
	PhaseDegraded VNodePhase = "Degraded"
	PhaseDeleting VNodePhase = "Deleting"
	PhaseFailed   VNodePhase = "Failed"
)

// LinkMode 表示某条链路的实现模式。
// +kubebuilder:validation:Enum=veth;vxlan
type LinkMode string

const (
	LinkModeVeth  LinkMode = "veth"
	LinkModeVXLAN LinkMode = "vxlan"
)

// LinkState 表示某条链路的当前建立状态。
// +kubebuilder:validation:Enum=Pending;Established;Error
type LinkState string

const (
	LinkStatePending     LinkState = "Pending"
	LinkStateEstablished LinkState = "Established"
	LinkStateError       LinkState = "Error"
)

// LinkStatus 描述一条链路（按 uid 索引）的实际建立情况。
type LinkStatus struct {
	UID int64 `json:"uid"`

	// +optional
	PeerPod string `json:"peer_pod,omitempty"`

	// +optional
	State LinkState `json:"state,omitempty"`

	// +optional
	Mode LinkMode `json:"mode,omitempty"`

	// VXLAN 模式下使用，由 controller 集中分配。
	// +optional
	VNI uint32 `json:"vni,omitempty"`

	// 跨节点链路对端节点的 InternalIP。
	// +optional
	UnderlayIP string `json:"underlayIP,omitempty"`

	// +optional
	LastError string `json:"lastError,omitempty"`

	// +optional
	EstablishedAt *metav1.Time `json:"establishedAt,omitempty"`
}

// SkippedItem 沿用 meshnet 字段语义，记录因对端未就绪暂时跳过的链路。
type SkippedItem struct {
	LinkID  int64  `json:"link_id,omitempty"`
	PodName string `json:"pod_name,omitempty"`
}

// VNodeStatus 是 VNode 的实际运行状态。
type VNodeStatus struct {
	// +optional
	Phase VNodePhase `json:"phase,omitempty"`

	// 对应已被 controller 处理过的最新 spec 版本。
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	HostIP string `json:"hostIP,omitempty"`

	// +optional
	HostNode string `json:"hostNode,omitempty"`

	// Pod 的 netns 路径（沿用 meshnet 字段名）。
	// +optional
	NetNs string `json:"netNs,omitempty"`

	// sandbox 容器 ID，pod 重建后会变化，agent 据此判断是否要重建链路。
	// +optional
	ContainerID string `json:"containerID,omitempty"`

	// Pod 主 IP（沿用）。
	// +optional
	SrcIP string `json:"srcIP,omitempty"`

	// +optional
	Skipped []SkippedItem `json:"skipped,omitempty"`

	// +optional
	LinkStatus []LinkStatus `json:"linkStatus,omitempty"`

	// ConfigHash 是 controller 当前期望的 ConfigMap 内容指纹（hex sha256）。
	// 每次 controller 渲染并 ensureConfigMaps 后写入。agent 监听到它变化时
	// 会 docker exec 主容器执行 reload 命令。
	// +optional
	ConfigHash string `json:"configHash,omitempty"`

	// ServiceReload 描述本 VNode 主容器服务的最近一次 reload 状态（M4）。
	// agent 收到 ConfigHash 变化后 exec 容器执行 role-specific reload 命令；
	// 这里反映那次执行的结果。
	// +optional
	ServiceReload *ServiceReloadStatus `json:"serviceReload,omitempty"`

	// 标准 K8s conditions：LinksConverged / PodReady / Validated 等。
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// ReloadState 描述 reload 命令执行的状态。
// +kubebuilder:validation:Enum=Pending;Success;Failed;NotApplicable
type ReloadState string

const (
	ReloadPending       ReloadState = "Pending"
	ReloadSuccess       ReloadState = "Success"
	ReloadFailed        ReloadState = "Failed"
	ReloadNotApplicable ReloadState = "NotApplicable" // role 不需要 reload（如 host）
)

// ServiceReloadStatus 一次 reload 执行的快照。
type ServiceReloadStatus struct {
	// 这次 reload 对应的 ConfigHash（应当等于本 reload 完成时的 status.configHash）。
	ObservedHash string `json:"observedHash,omitempty"`
	// 状态。
	State ReloadState `json:"state,omitempty"`
	// 命令（仅展示用，便于 kubectl describe 排障）。
	Command []string `json:"command,omitempty"`
	// 失败时的错误信息（含 stderr 截断）。
	// +optional
	Message string `json:"message,omitempty"`
	// 完成时间。
	// +optional
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`
}

// ============================================================================
//  Root types
// ============================================================================

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=vnodes,scope=Namespaced,shortName=vn
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Role,type=string,JSONPath=`.spec.role`
// +kubebuilder:printcolumn:name=DC,type=string,JSONPath=`.spec.dataCenter`
// +kubebuilder:printcolumn:name=Node,type=string,JSONPath=`.status.hostNode`
// +kubebuilder:printcolumn:name=Phase,type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name=Age,type=date,JSONPath=`.metadata.creationTimestamp`

// VNode 描述一个虚拟网络节点（virtual node）及其所有出边链路。
type VNode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VNodeSpec   `json:"spec,omitempty"`
	Status VNodeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// VNodeList 是 VNode 的集合。
type VNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VNode `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VNode{}, &VNodeList{})
}
