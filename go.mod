module github.com/bupt-aiops/vntopo-operator

go 1.22

require (
	github.com/go-logr/logr v1.4.2
	github.com/redhat-nfvpe/koko v0.0.0-20210414175119-3722cba9c4e8
	github.com/spf13/pflag v1.0.5
	github.com/vishvananda/netlink v1.2.1-beta.2
	github.com/vishvananda/netns v0.0.4
	google.golang.org/grpc v1.64.0
	google.golang.org/protobuf v1.34.2
	k8s.io/api v0.30.3
	k8s.io/apimachinery v0.30.3
	k8s.io/client-go v0.30.3
	k8s.io/utils v0.0.0-20240711033017-18e509b52bc8
	sigs.k8s.io/controller-runtime v0.18.4
)

// 真实依赖版本以 `go mod tidy` 结果为准；上述为 kubebuilder v4 / k8s 1.30 推荐组合。
