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

	// 标准 K8s conditions：LinksConverged / PodReady / Validated 等。
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
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
