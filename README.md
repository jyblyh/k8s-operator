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

**M3：跨节点 VXLAN 数据平面** — ✓ 完成（host1↔host2 跨节点 ping 通）

**M4：真实多节点拓扑（host + router）** — ✓ 代码完成，待集群验证

变更要点：

- agent reconciler 加自定义 `vnTopologyChanged` predicate：仅在 spec generation /
  status.hostNode / status.hostIP 变化时触发，linkStatus / conditions 变化不触发，
  避免反馈环
- agent reconciler 加反向 watch：任意 VNode 的 hostNode/hostIP/spec 变化 → 把它的
  peer 全部入队。专门解决"router 默认调度后 host 节点必须等 60s drift scan
  才知道 router 落到哪"的滞后问题
- `MakeVethPair` / `MakeVxlanLink` 返回 `(created bool, err)`，setup_handler 用它
  决定日志级别——真新建打 Info，幂等命中打 V(1)，日志噪音明显下降
- 新增示例 `examples/two-host-one-router.yaml`：host1 (worker A) ↔ r1 (默认调度)
  ↔ host2 (worker B) 三跳拓扑，r1 用真实 firewall-v2 镜像 + ip_forward 转发

**M5：主容器自动注入命令 + ConfigMap** — 计划中（让 controller 自动给 router/sw/dhcp/dns 等节点生成启动命令和挂载 ConfigMap，参考原项目 `microops-fe` 的逻辑）

**M6：webhook 校验 + e2e + Prometheus 指标** — 计划中

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

## 验证 M4（host + router 三跳）

```bash
# agent 镜像变更（reconciler predicate / 反向 watch）
make docker-build-agent docker-push-agent
kubectl -n vntopo-system rollout restart daemonset vntopo-agent

# 部署示例：host1 / host2 在不同 worker，r1 让 K8s 调度
kubectl apply -f examples/two-host-one-router.yaml

# 等三个 Pod Ready
kubectl -n demo-r get pods -o wide

# 验证三跳：host1 → r1 直连 → r1 → host2
kubectl -n demo-r exec host1 -c pod -- ping -c 3 10.0.1.1   # host1 ↔ r1.eth1
kubectl -n demo-r exec host1 -c pod -- ping -c 3 10.0.2.1   # host1 → r1 内转发到 r1.eth2
kubectl -n demo-r exec host1 -c pod -- ping -c 3 10.0.2.10  # host1 → r1 → host2 三跳

# 看路由器内部转发计数（每次 ping 计数会涨）
kubectl -n demo-r exec r1 -c pod -- cat /proc/net/dev

# 验证反向 watch 起作用（router status.hostNode 变 → host 立刻被 enqueue）
kubectl -n vntopo-system logs -l app.kubernetes.io/component=agent --tail=200 \
  | grep -E "vxlan link established|veth link established"
# 期望看到 host1 / host2 / r1 三方在 r1 调度完成后几秒内全部 established
```

## 验证 M2 / M3（保留作回归测试）

```bash
kubectl apply -f examples/single-host-veth.yaml
kubectl -n demo exec host1 -c pod -- ping -c 3 10.0.0.2

kubectl apply -f examples/cross-host-vxlan.yaml
kubectl -n demo-x exec host1 -c pod -- ping -c 3 10.1.0.2
```
