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
| 跨节点链路 | 每条 link 独立 P2P VXLAN（VNI 由 `fnv32(namespace+uid)` 确定性算出，无需协商） |
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
│  elected        │  · status 聚合  │                      │         │ · P2P vxlan        │
│                 │                 │                      │         │   (cross host)     │
│                 │                 │                      │         │ · drift scan 自愈  │
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

**M2：同节点 veth 数据平面** — ✓ 完成（host1↔host2 同节点 ping 通）

**M3：跨节点 VXLAN 数据平面** — ✓ 代码完成，待集群验证

变更要点：

- 跨节点 link 走 **P2P unicast VXLAN**：双方各起一个 vxlan 设备 push 进 pod ns
- VNI 由 `ComputeVNI(namespace, uid)` **确定性**算出，无需 controller 集中分配（24-bit fnv32 哈希）
- agent 启动时 **自动探测 underlay**：优先用 `--underlay-iface`，否则 `NODE_IP` 反查网卡，再次默认路由兜底
- 对端节点 IP 通过 K8s Node API 拿 InternalIP，作为 vxlan remote
- 每端 agent 只操作本节点 pod ns（VXLAN 是双向独立的，不需要进对端 ns）
- 新增 **DriftScanner**：60s 周期扫描本节点所有 VNode 重新 enqueue，自愈外部破坏（如手动 `ip link del`）
- `LinksConverged` Condition 现在 veth + vxlan 一视同仁，全部 Established 才置 True

**M4：webhook 校验 + e2e + Prometheus 指标** — 计划中

详见 [`docs/roadmap.md`](docs/roadmap.md)。

## 验证 M3

```bash
# 0. agent 镜像变了（新增 underlay 探测、vxlan、drift scan），重打推送
make docker-build-agent docker-push-agent

# 1. 重新部署
make deploy

# 2. 部署 cross-host 示例（注意 nodeSelector 要改成你集群里实际两个 worker 名）
kubectl apply -f examples/cross-host-vxlan.yaml

# 3. 等 Pod Ready 后跨节点 ping
kubectl -n demo-x get pods -o wide   # 确认 host1/host2 落在不同 worker
kubectl -n demo-x exec host1 -c pod -- ping -c 3 10.1.0.2
# 期望：3 packets transmitted, 3 received, 0% packet loss

# 4. 检查 status.linkStatus 中的 vxlan 信息
kubectl -n demo-x get vn host1 -o yaml | sed -n '/linkStatus:/,/conditions:/p'
# 期望看到：
#   linkStatus:
#     - uid: 1
#       peer_pod: host2
#       state: Established
#       mode: vxlan
#       vni: <非零 24-bit 数>
#       underlayIP: <对端 worker 的 InternalIP>

# 5. 看 vxlan 设备
kubectl -n demo-x exec host1 -c pod -- ip -d link show host1_host2
# 期望看到 type vxlan，id=VNI，dstport 4789，remote 是对端节点 IP

# 6. 抓 vxlan 包验证（在 host1 所在节点上）
sudo tcpdump -ni eth0 'udp port 4789' -c 5
```

## 验证 M2（保留作回归测试）

```bash
kubectl apply -f examples/single-host-veth.yaml
kubectl -n demo exec host1 -c pod -- ping -c 3 10.0.0.2
```
