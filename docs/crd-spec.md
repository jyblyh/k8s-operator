# CRD 字段说明：VNode

> Group/Version/Kind: `vntopo.bupt.site/v1alpha1`, `VNode`
> Scope: Namespaced
> Shortname: `vn`

## Spec

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `role` | string (enum) | 是 | 节点角色，可选值：`host` / `sw` / `asw` / `csw` / `r` / `fw` / `dhcp` / `dns` / `ws`。controller 根据 role 决定一些默认行为（如 `r` 允许不填 `dataCenter`）。 |
| `dataCenter` | string | 否 | 所属数据中心标识。同 DC 的所有 VNode 必须共享同一份 `nodeSelector`（webhook 校验）。`role=r` 用作 inter-DC 路由器时可留空。 |
| `nodeSelector` | map[string]string | 视情况 | 显式节点选择。同 DC 的非路由器节点**必须**填，且必须与同 DC 其它 VNode 一致。`role=r` 且 `dataCenter` 空时可不填，由 K8s 自由调度。 |
| `affinity` | corev1.Affinity | 否 | 高级亲和性，与 `nodeSelector` 二选一/可叠加。 |
| `template` | corev1.PodTemplateSpec | 是 | Pod 模板。controller 注入 `ownerReferences` / `nodeSelector` / `affinity` / `labels` / `initContainers`。用户写的 `containers` / `volumes` / `command` 等字段全部保留。 |
| `localIp` | string | 否 | 兼容字段，部分组件读取 pod 主 IP 用。 |
| `links` | []LinkSpec | 否 | 本 VNode 对外的所有点对点链路。 |

### LinkSpec

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `uid` | int64 | 是 | 链路唯一 ID（namespace 内）。两端必须用同一个 uid。 |
| `peer_pod` | string | 是 | 对端 VNode 的 name（同 namespace）。 |
| `local_intf` | string | 是 | 本端在 pod netns 内的接口名，≤15 字符。 |
| `peer_intf` | string | 是 | 对端在 pod netns 内的接口名，≤15 字符。 |
| `local_ip` | string | 否 | 本端接口 IP（CIDR 格式，如 `10.0.0.1/24`）。 |
| `peer_ip` | string | 否 | 对端接口 IP（信息字段，便于上层应用读取，agent 不写对端）。 |
| `cost` | float | 否 | 路由权重等指标。 |
| `metrics` | LinkMetrics | 否 | 带宽、延迟、抖动、丢包率，运维可见。 |

### LinkMetrics

| 字段 | 类型 | 说明 |
|---|---|---|
| `bandwidth_mbps` | float | |
| `jitter_ms` | float | |
| `latency_ms` | float | |
| `loss_percentage` | float | |
| `last_updated` | string (RFC3339) | |

## Status

| 字段 | 类型 | 说明 |
|---|---|---|
| `phase` | string | `Pending` / `Creating` / `Ready` / `Degraded` / `Deleting` / `Failed` |
| `observedGeneration` | int64 | 标准模式，匹配 `metadata.generation` 表示 controller 已处理最新 spec |
| `hostIP` | string | Pod 所在节点的 InternalIP |
| `hostNode` | string | Pod 所在节点的 nodeName（`spec.nodeName`） |
| `netNs` | string | Pod 的 netns 路径，沿用 meshnet 字段名 |
| `containerID` | string | sandbox 容器 ID，pod 重建会变 |
| `srcIP` | string | Pod 主 IP（沿用） |
| `skipped` | []SkippedItem | 沿用 meshnet 字段，记录因对端未就绪暂时跳过的链路 |
| `linkStatus` | []LinkStatus | 每条链路的实际状态 |
| `conditions` | []metav1.Condition | 标准 conditions（`LinksConverged` / `PodReady` / `Validated`） |

### LinkStatus

| 字段 | 类型 | 说明 |
|---|---|---|
| `uid` | int64 | 对应 `spec.links[*].uid` |
| `peer_pod` | string | 冗余便于查看 |
| `state` | string | `Pending` / `Established` / `Error` |
| `mode` | string | `veth` / `vxlan` |
| `vni` | uint32 | 仅 vxlan 模式，由 controller 分配 |
| `underlayIP` | string | 仅 vxlan 模式，对端节点的 InternalIP |
| `lastError` | string | 最近一次失败原因 |
| `establishedAt` | string (RFC3339) | 链路最近一次进入 Established 的时间 |

## Printer Columns（`kubectl get vnodes`）

| 列 | jsonPath |
|---|---|
| Role | `.spec.role` |
| DC | `.spec.dataCenter` |
| Node | `.status.hostNode` |
| Phase | `.status.phase` |
| Age | `.metadata.creationTimestamp` |

## 校验规则（webhook）

1. `role ∈ {host, sw, asw, csw, r, fw, dhcp, dns, ws}`
2. `local_intf` / `peer_intf` ≤15 字符且符合 Linux 接口命名规则
3. 同 namespace 内 `links[].uid` 不重复
4. 同 `spec.dataCenter`（非空）的所有 VNode `spec.nodeSelector` 必须一致
5. `role != r` 且 `dataCenter` 非空时，`nodeSelector` 必填
6. `peer_pod` 引用的 VNode 不存在时给 warning（不阻断创建，允许"先建一端"的场景）

## 示例

### 单节点 host-host（same-host veth）

见 [`examples/single-host-veth.yaml`](../examples/single-host-veth.yaml)

### 跨节点 router-router（cross-host vxlan）

见 [`examples/cross-host-vxlan.yaml`](../examples/cross-host-vxlan.yaml)
