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
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/vishvananda/netlink v1.1.0
	github.com/vishvananda/netns v0.0.4
	k8s.io/api v0.23.5
	k8s.io/apimachinery v0.23.5
	k8s.io/client-go v0.23.5
	k8s.io/utils v0.0.0-20211116205334-6203023598ed // indirect
	sigs.k8s.io/controller-runtime v0.11.2
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.1.1 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/evanphx/json-patch v4.12.0+incompatible // indirect
	github.com/fsnotify/fsnotify v1.5.1 // indirect
	github.com/go-logr/zapr v1.2.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/golang/protobuf v1.5.2 // indirect
	github.com/google/go-cmp v0.5.5 // indirect
	github.com/google/gofuzz v1.1.0 // indirect
	github.com/google/uuid v1.1.2 // indirect
	github.com/googleapis/gnostic v0.5.5 // indirect
	github.com/imdario/mergo v0.3.12 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.2-0.20181231171920-c182affec369 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/prometheus/client_golang v1.11.0 // indirect
	github.com/prometheus/client_model v0.2.0 // indirect
	github.com/prometheus/common v0.28.0 // indirect
	github.com/prometheus/procfs v0.6.0 // indirect
	go.uber.org/atomic v1.7.0 // indirect
	go.uber.org/multierr v1.6.0 // indirect
	go.uber.org/zap v1.19.1 // indirect
	golang.org/x/net v0.0.0-20211209124913-491a49abca63 // indirect
	golang.org/x/oauth2 v0.0.0-20210819190943-2bc19b11175f // indirect
	golang.org/x/sys v0.2.0 // indirect
	golang.org/x/term v0.0.0-20210615171337-6886f2dfbf5b // indirect
	golang.org/x/text v0.3.7 // indirect
	golang.org/x/time v0.0.0-20210723032227-1f47c861a9ac // indirect
	gomodules.xyz/jsonpatch/v2 v2.2.0 // indirect
	google.golang.org/appengine v1.6.7 // indirect
	google.golang.org/protobuf v1.27.1 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.0-20210107192922-496545a6307b // indirect
	k8s.io/apiextensions-apiserver v0.23.5 // indirect
	k8s.io/component-base v0.23.5 // indirect
	k8s.io/klog/v2 v2.30.0 // indirect
	k8s.io/kube-openapi v0.0.0-20211115234752-e816edb12b65 // indirect
	sigs.k8s.io/json v0.0.0-20211020170558-c049b76a60c6 // indirect
	sigs.k8s.io/structured-merge-diff/v4 v4.2.1 // indirect
	sigs.k8s.io/yaml v1.3.0 // indirect
)
