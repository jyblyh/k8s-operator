module github.com/jyblyh/k8s-operator

// grpc v1.58+ 要求 Go 1.19+，所以这里至少 1.19。
// 实际编译用 Go 1.20/1.21/1.22 都能过。
go 1.19

// =====================================================================
// 依赖版本固定为 K8s v1.21 兼容范围。
//
// 选型理由：
//   · k8s.io/* v0.23.5 是官方 skew policy 允许覆盖 1.21 server 的最远客户端版本
//     （client 至多比 server 高 1 个 minor，v0.23.x → 1.21–1.23 server）。
//   · sigs.k8s.io/controller-runtime v0.11.2 明确支持 K8s 1.21–1.23，
//     并且自带 metav1.Condition / meta.SetStatusCondition 等我们用到的 API。
//   · vishvananda/netlink v1.1.0 + netns v0.0.4：M2 同节点 veth 创建用，
//     社区主流且稳定。MakeVethPair 内部直接调 LinkAdd / LinkSetNsFd，
//     不再引入 koko 这一层薄封装（依赖更少、行为更可控）。
//   · grpc 在 M2 已经不直接使用（init↔agent 改成 net/rpc + jsonrpc），
//     但 controller-runtime 的间接依赖仍然可能拉它，留着不影响。
//
// 注意：
//   · 真实依赖列表与子依赖以 `go mod tidy` 结果为准。
//   · 集群升级到 1.24+ 后，可同步上调到 controller-runtime v0.13+ / k8s 0.25+，
//     并把 cri 接口从 docker shim 迁到 containerd CRI。
// =====================================================================
require (
	github.com/go-logr/logr v1.2.0
	github.com/spf13/pflag v1.0.5
	github.com/vishvananda/netlink v1.1.0
	github.com/vishvananda/netns v0.0.4
	k8s.io/api v0.23.5
	k8s.io/apimachinery v0.23.5
	k8s.io/client-go v0.23.5
	k8s.io/utils v0.0.0-20211116205334-6203023598ed
	sigs.k8s.io/controller-runtime v0.11.2
)
