# Image registry & tags
REGISTRY        ?= docker.io/bupt-aiops
TAG             ?= dev
IMG_CONTROLLER  ?= $(REGISTRY)/vntopo-controller:$(TAG)
IMG_AGENT       ?= $(REGISTRY)/vntopo-agent:$(TAG)
IMG_INIT        ?= $(REGISTRY)/vntopo-init:$(TAG)

# Tooling versions
CONTROLLER_GEN_VERSION ?= v0.15.0
KUSTOMIZE_VERSION      ?= v5.4.2

# 本机 go bin 目录
LOCALBIN        ?= $(shell pwd)/bin
CONTROLLER_GEN  ?= $(LOCALBIN)/controller-gen
KUSTOMIZE       ?= $(LOCALBIN)/kustomize

##@ General

.PHONY: help
help:                   ## 显示帮助
	@grep -E '^[a-zA-Z_-]+:.*?##' $(MAKEFILE_LIST) | awk 'BEGIN {FS=":.*##"}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

##@ Codegen

.PHONY: tools
tools: $(CONTROLLER_GEN) $(KUSTOMIZE)   ## 安装 controller-gen / kustomize 到 ./bin

$(LOCALBIN):
	mkdir -p $(LOCALBIN)

$(CONTROLLER_GEN): $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

$(KUSTOMIZE): $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION)

.PHONY: generate
generate: $(CONTROLLER_GEN)             ## 生成 deepcopy 函数
	$(CONTROLLER_GEN) object:headerFile=hack/boilerplate.go.txt paths="./api/..."

.PHONY: manifests
manifests: $(CONTROLLER_GEN)            ## 生成 CRD / RBAC YAML
	# crd:allowDangerousTypes=true：放行 LinkMetrics / Cost 等 float64 字段
	# （这些是 ping_exporter/采集器回填的运行时指标，不参与调度，可接受 JSON
	#  数字跨语言精度差；如果后续要更严格再换成 string 或 resource.Quantity）
	$(CONTROLLER_GEN) crd:allowDangerousTypes=true rbac:roleName=vntopo-controller-role webhook \
	    paths="./..." \
	    output:crd:artifacts:config=config/crd/bases \
	    output:rbac:artifacts:config=config/rbac

.PHONY: proto
proto:                                  ## 生成 netservice gRPC 代码（需要 protoc + 插件）
	protoc --go_out=. --go_opt=paths=source_relative \
	       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
	       internal/netservice/netservice.proto

##@ Build

.PHONY: fmt vet tidy
fmt:    ; go fmt ./...
vet:    ; go vet ./...
tidy:   ; go mod tidy

.PHONY: test
test: generate fmt vet                  ## 运行单元测试
	go test ./... -coverprofile cover.out

.PHONY: build-controller build-agent build-init
build-controller:
	go build -o bin/vntopo-controller ./cmd/controller
build-agent:
	go build -o bin/vntopo-agent ./cmd/agent
build-init:
	go build -o bin/vntopo-init ./cmd/init

.PHONY: build
build: build-controller build-agent build-init   ## 编译三个二进制

##@ Docker

.PHONY: docker-build docker-build-controller docker-build-agent docker-build-init
docker-build-controller:
	docker build -t $(IMG_CONTROLLER) -f Dockerfile .
docker-build-agent:
	docker build -t $(IMG_AGENT) -f Dockerfile.agent .
docker-build-init:
	docker build -t $(IMG_INIT) -f Dockerfile.init .

docker-build: docker-build-controller docker-build-agent docker-build-init  ## 构建三个镜像

.PHONY: docker-push
docker-push:                            ## 推送镜像
	docker push $(IMG_CONTROLLER)
	docker push $(IMG_AGENT)
	docker push $(IMG_INIT)

##@ Deploy

# install / deploy / undeploy 不依赖 manifests / controller-gen，
# 因为 CRD YAML 已 commit 进 git。这样这些 target 可以在 master 节点（没装 Go）上直接跑。
# 改了 api/ 下 Go 类型时，请先在虚拟机上 `make manifests` 然后 commit + push。

.PHONY: install
install: $(KUSTOMIZE)                   ## 仅安装 CRD
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

.PHONY: uninstall
uninstall: $(KUSTOMIZE)                 ## 卸载 CRD
	$(KUSTOMIZE) build config/crd | kubectl delete --ignore-not-found -f -

.PHONY: deploy
deploy: $(KUSTOMIZE)                    ## 部署 controller + agent + RBAC + namespace
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(IMG_CONTROLLER)
	cd config/agent   && $(KUSTOMIZE) edit set image agent=$(IMG_AGENT) init=$(IMG_INIT)
	# 把 deployment.yaml 里 VNTOPO_INIT_IMAGE env 占位符替换为最终 init 镜像。
	# 注意：sed 直接改源文件，commit 前看下 git diff 别误推。
	$(KUSTOMIZE) build config/default \
	    | sed "s|__INIT_IMAGE__|$(IMG_INIT)|g" \
	    | kubectl apply -f -

.PHONY: undeploy
undeploy: $(KUSTOMIZE)                  ## 卸载所有资源
	$(KUSTOMIZE) build config/default | kubectl delete --ignore-not-found -f -
