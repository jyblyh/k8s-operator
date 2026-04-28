module github.com/jyblyh/k8s-operator

// 工具链最低版本：用 Go 1.20+ 编译都能过；当前推荐 1.20 / 1.21 / 1.22 都行。
go 1.18

// =====================================================================
// 依赖版本固定为 K8s v1.21 兼容范围。
// 选型理由：
//   · k8s.io/* v0.23.5 是官方 skew policy 允许覆盖 1.21 server 的最远客户端版本
//     （client 至多比 server 高 1 个 minor，v0.23.x → 1.21–1.23 server）。
//   · sigs.k8s.io/controller-runtime v0.11.2 明确支持 K8s 1.21–1.23，
//     并且自带 metav1.Condition / meta.SetStatusCondition 等我们用到的 API。
//   · 后续如果集群升级到 1.24+，可同步上调到 controller-runtime v0.13+ / k8s 0.25+。
// =====================================================================
require (
	github.com/go-logr/logr v1.2.0
	github.com/redhat-nfvpe/koko v0.0.0-20210414175119-3722cba9c4e8
	github.com/spf13/pflag v1.0.5
	github.com/vishvananda/netlink v1.1.1-0.20210330154013-f5de75959ad5
	github.com/vishvananda/netns v0.0.0-20210104183010-2eb08e3e575f
	google.golang.org/grpc v1.40.0
	google.golang.org/protobuf v1.27.1
	k8s.io/api v0.23.5
	k8s.io/apimachinery v0.23.5
	k8s.io/client-go v0.23.5
	k8s.io/utils v0.0.0-20211116205334-6203023598ed
	sigs.k8s.io/controller-runtime v0.11.2
)

// 真实依赖列表与子依赖以 `go mod tidy` 结果为准。
