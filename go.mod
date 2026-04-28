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
//   · grpc v1.58.3 / protobuf v1.31.0：解决 google.golang.org/genproto
//     在 2024 年拆分子模块后，旧 grpc + 新 koko 同时拉两份导致的 ambiguous import。
//   · 后续如果集群升级到 1.24+，可同步上调到 controller-runtime v0.13+ / k8s 0.25+。
//
// 注意：
//   · netlink / netns / koko 等"真正用 netlink 操作宿主机网络"的库，
//     等 M1 写 internal/agent/netlink_veth.go 时再加进来。
//     届时建议用 p2pnet 同款 `github.com/karkar0813/koko`（fork，比 redhat-nfvpe/koko 活跃）。
//   · 真实依赖列表与子依赖以 `go mod tidy` 结果为准。
// =====================================================================
require (
	github.com/go-logr/logr v1.2.0
	github.com/spf13/pflag v1.0.5
	google.golang.org/grpc v1.58.3
	google.golang.org/protobuf v1.31.0
	k8s.io/api v0.23.5
	k8s.io/apimachinery v0.23.5
	k8s.io/client-go v0.23.5
	k8s.io/utils v0.0.0-20211116205334-6203023598ed
	sigs.k8s.io/controller-runtime v0.11.2
)
