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

**M0：脚手架** — ✓ 完成

**M1：controller 主路径** — ✓ 完成

- 完整 CRD（VNode）注册并 print column 配置好
- `validateSpec`、`ensurePod`、`syncStatus + derivePhase + conditions` 全部跑通
- OwnerReference 级联删除验证通过
- 单节点 example: host1 + host2 都能调度到指定 worker 并 Running

**M2：同节点 veth 数据平面** — ✓ 代码完成，待集群验证

变更要点：

- init 容器恢复了真正的 RPC 调用（M1 是 no-op）
- agent ↔ init 协议改成 **net/rpc + jsonrpc on unix socket**（替代原 gRPC 方案，零 protoc 依赖）
- agent 内置 **WorkerPool**：RPC 立即返回 `queued`，建链异步进行
- 同节点 veth 由 **vishvananda/netlink + netns** 直接实现（不再引入 koko）
- agent 通过 docker.sock REST API 找 Pod sandbox PID，定位 netns
- VNode.status.linkStatus[] 实时回写每条 link 的 `state / mode / lastError`
- `LinksConverged` Condition 表示是否本节点所有 link 都建好

**M3：跨节点 VXLAN** — 计划中（每条 link 独立 VNI、underlay 自动探测、drift 扫描自愈）

详见 [`docs/roadmap.md`](docs/roadmap.md)。

## 验证 M2

```bash
# 0. 把 agent + init 镜像重新打包推到 ACR（go.mod 加了 netlink/netns）
make docker-build-agent docker-push-agent
make docker-build-init  docker-push-init

# 1. 集群已经在跑 M1 的话，重新 deploy（CRD 没变，只滚 Deployment / DaemonSet）
make deploy

# 2. 部署示例
kubectl apply -f examples/single-host-veth.yaml

# 3. 等 Pod Ready，进 host1 ping host2
kubectl -n demo exec host1 -c pod -- ping -c 3 10.0.0.2
# 期望：3 packets transmitted, 3 received, 0% packet loss

# 4. 看 status.linkStatus 验证 controller/agent 协同
kubectl -n demo get vn host1 -o yaml | sed -n '/status:/,$p'
# 期望看到：
#   linkStatus:
#     - uid: 1
#       peer_pod: host2
#       state: Established
#       mode: veth
#       establishedAt: ...
#   conditions:
#     - type: LinksConverged
#       status: "True"
#       reason: AllVethEstablished
```
