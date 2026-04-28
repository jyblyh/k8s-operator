# vntopo-operator

> Virtual Network Topology Operator for Kubernetes — 替代现有 `aiops-evaluation` + `p2pnet` 的网络仿真部署链路。

## 项目目标

将"按拓扑创建虚拟网络节点 Pod、并在 Pod 之间建立点对点链路"这一套逻辑收口到一个 Kubernetes Operator 中：

- **CRD 驱动**：用户/前端只生成 `VNode` CR，不再直接生成 Pod / ConfigMap / Topology YAML。
- **跨节点支持**：同节点用 `veth pair`，跨节点用 **每条链路独立 VXLAN**。
- **数据中心亲和**：同 DC 的所有设备（host/server/sw/asw/csw/fw）落到同一个 K8s worker，路由器由 K8s 自由调度。
- **生命周期完整**：finalizer + ownerReference + status 聚合 + 漂移自愈。

## 关键决策（已敲定）

| 决策项 | 选择 |
|---|---|
| CRD group | `vntopo.bupt.site` |
| CRD version | `v1alpha1` |
| CRD kind | `VNode`（Namespaced） |
| Operator 部署 namespace | `vntopo-system`（独立） |
| 跨节点链路 | 每条 link 独立 VXLAN（VNI 由 controller 分配） |
| Pod 模板 | 内嵌在 `VNode.spec.template` |
| Pod 启动触发链路建立 | **initContainer 模式**（不动集群 Calico CNI 配置） |
| 跨节点 daemon 基础 | fork [`networkop/meshnet-cni`](https://github.com/networkop/meshnet-cni)，**砍掉 CNI plugin 部分**，保留 daemon |
| init 容器基础 | 直接复用 `p2pnet/client` |
| 控制器 HA | leader election，支持多副本 |
| 双向链路一致性 | v1 严格策略（缺失则 Degraded），v2 加自愈 |

## 架构总览

```
microops-fe  ──► aiops-evaluation  ──► kube-apiserver (apply VNode CR)
                                              │
        ┌─────────────────────────────────────┼─────────────────────────────────┐
        ▼                                     ▼                                 ▼
┌─────────────────┐               ┌──────────────────────┐         ┌────────────────────┐
│ vntopo-controller│ watch VNode/Pod│ kube-apiserver       │watchVNode│ vntopo-agent       │
│ (Deployment)    │  · ensure Pod   │                      │◄────────│ (DaemonSet)        │
│  HA, leader-    │  · finalizer    │                      │         │ · veth (same host) │
│  elected        │  · status 聚合  │                      │         │ · vxlan + bridge   │
│                 │  · VNI 分配     │                      │         │   (cross host)     │
└─────────────────┘               └──────────────────────┘         └────────────────────┘
                                                                            ▲
                                                                            │ unix socket
                                                                            │
                                                              ┌─────────────┴────────────┐
                                                              │ vntopo-init (init Pod)   │
                                                              │ 通知本节点 agent 建链    │
                                                              └──────────────────────────┘
```

详细设计见 [`docs/architecture.md`](docs/architecture.md)。

## 仓库布局

```
vntopo-operator/
├── README.md                          # 本文件
├── PROJECT                            # kubebuilder 元数据
├── Makefile                           # 构建 / 部署 / 生成代码
├── go.mod                             # 由 kubebuilder init 生成
├── Dockerfile                         # controller 镜像
├── Dockerfile.agent                   # agent (DaemonSet) 镜像
├── Dockerfile.init                    # init 容器镜像
│
├── api/v1alpha1/                      # CRD Go 类型定义
│   ├── groupversion_info.go
│   ├── vnode_types.go                 # ★ 核心：VNode types
│   └── zz_generated.deepcopy.go       # make generate 产物
│
├── cmd/                               # 三个二进制入口
│   ├── controller/main.go
│   ├── agent/main.go
│   └── init/main.go
│
├── internal/
│   ├── controller/                    # controller 业务逻辑
│   │   ├── vnode_controller.go        # ★ Reconciler
│   │   ├── pod_renderer.go            # 模板渲染 / 注入
│   │   ├── vni_allocator.go           # VNI 分配
│   │   └── webhook/                   # admission webhook
│   ├── agent/                         # agent 业务逻辑
│   │   ├── reconciler.go
│   │   ├── netlink_veth.go            # same-host veth (复用 koko)
│   │   ├── netlink_vxlan.go           # cross-host vxlan + bridge
│   │   ├── nsutil.go
│   │   └── locks.go
│   ├── initclient/                    # init 容器逻辑（来自 p2pnet/client）
│   ├── netservice/                    # init <-> agent gRPC
│   │   ├── netservice.proto
│   │   └── netservice.pb.go
│   └── common/
│       ├── consts.go                  # finalizer / label keys
│       ├── naming.go                  # 接口命名工具
│       └── vni.go                     # VNI 哈希函数
│
├── config/                            # kustomize 部署清单
│   ├── namespace.yaml                 # vntopo-system
│   ├── crd/bases/                     # ★ CRD YAML
│   ├── manager/                       # controller Deployment
│   ├── agent/                         # agent DaemonSet
│   ├── rbac/                          # ServiceAccount / Role / RoleBinding
│   ├── webhook/                       # admission webhook
│   └── default/                       # kustomization 总入口
│
├── docs/
│   ├── architecture.md                # ★ 架构详细设计
│   ├── crd-spec.md                    # ★ CRD 字段一览
│   └── roadmap.md                     # 里程碑路线图
│
├── hack/                              # 工具脚本
│   ├── boilerplate.go.txt
│   └── tools.go
│
├── test/                              # e2e 测试
│   └── e2e/
│
└── examples/                          # 示例 CR
    ├── single-host-veth.yaml
    └── cross-host-vxlan.yaml
```

## 当前状态

**M0：脚手架阶段**

- [x] 设计文档定稿
- [x] CRD YAML 草稿
- [x] `vnode_types.go` 草稿
- [ ] `kubebuilder init` 生成项目骨架并合并草稿
- [ ] Makefile / Dockerfile
- [ ] 基础 RBAC / Deployment / DaemonSet manifest
- [ ] CI（lint / test / image build）

后续里程碑见 [`docs/roadmap.md`](docs/roadmap.md)。

## 下一步

```bash
# 1. 安装 kubebuilder（v3+）和 controller-gen
# 2. 在本目录初始化项目（会与现有草稿合并）
kubebuilder init --domain bupt.site --repo github.com/<your-org>/vntopo-operator
kubebuilder create api --group vntopo --version v1alpha1 --kind VNode --resource --controller
# 3. 把 api/v1alpha1/vnode_types.go 草稿合并进 kubebuilder 生成的版本
make generate manifests
make docker-build IMG=vntopo-controller:dev
```
