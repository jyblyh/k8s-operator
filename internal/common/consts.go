/*
Copyright 2026 BUPT AIOps Lab.
*/

// Package common 收纳跨 controller / agent / init 的通用常量与工具。
package common

const (
	// CRD 元信息
	GroupName = "vntopo.bupt.site"
	Version   = "v1alpha1"

	// Finalizer 用于 VNode 删除联动。
	Finalizer = "vntopo.bupt.site/cleanup"

	// Label keys：所有 controller 注入到 Pod 上的标签均以此为前缀。
	LabelVNode = "vntopo.bupt.site/vnode"
	LabelRole  = "vntopo.bupt.site/role"
	LabelDC    = "vntopo.bupt.site/dc"

	// Annotation keys
	AnnotationManagedBy = "vntopo.bupt.site/managed-by"
	ManagedByValue      = "vntopo-controller"

	// Init container 名 / 镜像默认 tag。
	InitContainerName = "vntopo-init"

	// Agent 与 init 容器之间的本地通信 socket。
	AgentSocketPath = "/var/run/vntopo/agent.sock"
	AgentSocketDir  = "/var/run/vntopo"

	// Conditions 类型
	ConditionLinksConverged = "LinksConverged"
	ConditionPodReady       = "PodReady"
	ConditionValidated      = "Validated"

	// Reconcile 周期相关（单位：秒）
	DefaultRequeueShortSec = 2
	DefaultRequeueLongSec  = 30
	AgentDriftScanSec      = 60

	// VXLAN
	VXLANDefaultPort = 4789
	VXLANMTUOverhead = 50 // 跨节点 vxlan 设备 MTU 应当 = 节点 MTU - 50
)
