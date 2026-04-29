/*
Copyright 2026 BUPT AIOps Lab.
*/

// Package netservice 定义 vntopo-init <-> vntopo-agent 之间的 RPC 协议。
//
// 选型说明
// ========
// 这是一个**只在节点内部 unix socket 上传输**的 RPC：init 容器 → 同节点 agent。
// M2 选用 Go 标准库 `net/rpc` + `encoding/json/jsonrpc` 作为传输层，理由：
//
//	· 不需要跨语言互操作 → 不必引入 gRPC/protobuf 工具链
//	· 标准库实现 → 编译产物干净、依赖最少
//	· 调试方便 → 可以直接 `nc -U agent.sock` 手敲 JSON 验证
//	· 报文格式自描述 → 协议演进时只需向后兼容字段
//
// 如果未来 M3+ 真的需要跨节点 RPC（VXLAN 控制面），届时可以在另一个端口上
// 引入 gRPC，与本协议互不冲突。
//
// 协议语义
// ========
// `NetService.SetupLinks(SetupReq) -> (SetupResp, error)`
//
//   - 调用是 **fire-and-queue**：agent 收到请求后只把任务塞进 worker 队列，
//     立即返回 `Status=queued`。init 容器看到 queued 即 exit 0，让业务容器起来。
//   - 真正的 veth/vxlan 建链是 agent 异步执行的；如果失败，agent 会把错误信息
//     写到 `VNode.status.linkStatus[*].lastError`，由 controller 的 reconciler
//     看到后改 phase=Degraded。
//
// 这种"提交即返回"的设计参考 meshnet-cni / multus 的风格——CNI/init 阶段不能
// 阻塞太久，否则 kubelet 会判 Pod 启动超时。
package netservice

// ServiceName 是 net/rpc 注册时的服务名，client 端 Call 时拼成 "NetService.SetupLinks"。
const ServiceName = "NetService"

// MethodSetupLinks net/rpc 调用的方法名（拼接形式 "NetService.SetupLinks"）。
const MethodSetupLinks = ServiceName + ".SetupLinks"

// SetupReq 是 init 容器提交给 agent 的请求体。
//
// 信息已经全部能从 K8s API 查到（Pod ns/name 决定一切），但 init 容器为了
// 不依赖 ServiceAccount + apiserver，把这些必要字段直接通过 RPC 带过来。
type SetupReq struct {
	// Namespace VNode/Pod 所在 namespace。
	Namespace string `json:"namespace"`

	// PodName VNode 名 = Pod 名（同名约定）。
	PodName string `json:"pod_name"`

	// HostIP 由 init 通过 Downward API status.hostIP 注入，仅作 sanity check。
	HostIP string `json:"host_ip,omitempty"`
}

// SetupRespStatus 是 SetupResp.Status 的枚举字符串。
type SetupRespStatus string

const (
	// StatusQueued 任务已入 worker 队列，agent 异步建链。
	StatusQueued SetupRespStatus = "queued"

	// StatusDone agent 已经完成本 Pod 的所有同节点 link 建立（实际上 M2 不会
	// 同步等待，所以基本不会返回这个值；保留枚举位让协议可演进）。
	StatusDone SetupRespStatus = "done"

	// StatusError 请求层面就失败（比如参数缺失、worker 满了等）。
	StatusError SetupRespStatus = "error"
)

// SetupResp 是 agent 给 init 的响应体。
type SetupResp struct {
	Status  SetupRespStatus `json:"status"`
	Message string          `json:"message,omitempty"`
}
