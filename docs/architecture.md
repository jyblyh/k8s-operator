# 架构设计

## 1. 总体架构

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              控制平面                                         │
│                                                                              │
│   ┌───────────────────────────────┐       watch VNode / Pod                  │
│   │ vntopo-controller             │ ◄────────────────────────                │
│   │ Deployment, leader-elected    │                                          │
│   │  · ensure Pod (ownerRef)      │                                          │
│   │  · 注入 nodeSelector / init   │                                          │
│   │  · finalizer 处理删除联动     │                                          │
│   │  · 校验 dataCenter 一致性     │                                          │
│   │  · 分配 VNI                  │                                          │
│   │  · 聚合 status                │                                          │
│   └─────────────┬─────────────────┘                                          │
│                 │                                                            │
│                 │ (controller 不直接 RPC agent，靠 CR/status 协调)           │
└─────────────────┼────────────────────────────────────────────────────────────┘
                  │
                  ▼  CR / status
┌─────────────────────────────────────────────────────────────────────────────┐
│                              数据平面                                         │
│                                                                              │
│   每个 K8s worker 节点都跑一份：                                              │
│                                                                              │
│   ┌───────────────────────────────┐                                          │
│   │ vntopo-agent (DaemonSet)      │                                          │
│   │  · watch VNode (本节点 only)  │                                          │
│   │  · gRPC server (unix socket)  │ ◄─── vntopo-init initContainer           │
│   │    给 init 容器调用           │      (每个 VNode Pod 启动时调一次)       │
│   │  · diff & apply 链路：        │                                          │
│   │     - same host: veth (koko) │                                          │
│   │     - cross host: vxlan+br   │                                          │
│   │  · 周期 drift detect (60s)   │                                          │
│   │  · 写回 status.linkStatus    │                                          │
│   └───────────────────────────────┘                                          │
└─────────────────────────────────────────────────────────────────────────────┘
```

## 2. 组件职责

### 2.1 vntopo-controller（Deployment）

- 运行在 `vntopo-system` namespace。
- HA：`controller-runtime` 的 leader election，建议 2 副本。
- **不直接操作宿主机网络**，完全基于 K8s API 工作。

主要职责：

1. **VNode → Pod 的转换**：从 `spec.template` 渲染出 Pod，注入：
   - `metadata.ownerReferences` → VNode（删 VNode 自动 GC Pod）
   - `metadata.labels`：`vntopo.bupt.site/vnode`、`vntopo.bupt.site/role`、`vntopo.bupt.site/dc`
   - `spec.nodeSelector` / `spec.affinity`（来自 VNode）
   - `spec.initContainers`：追加 `vntopo-init` 容器
2. **finalizer 管理**：`vntopo.bupt.site/cleanup`
3. **删除联动**：被删除时 patch 所有 peer VNode 的 `spec.links` 移除自己的入口。
4. **VNI 分配**：跨节点 link 的 VNI 由 controller 集中分配，写到 `status.linkStatus[].vni`，两端 agent 都从这里读，避免不一致。
5. **status 聚合**：`phase` / `observedGeneration` / `hostIP` / `hostNode` / `containerID` / `linkStatus[]` / `conditions`。
6. **Admission Webhook**（v1 也实现）：
   - 校验同 `dataCenter` 的 VNode `nodeSelector` 一致
   - 校验 `local_intf / peer_intf` ≤ 15 字符
   - 校验 `peer_pod` 引用的 VNode 存在（创建时只警告，不强制）

### 2.2 vntopo-agent（DaemonSet）

- 运行在每个 K8s worker（`spec.template.spec.hostNetwork: true`、`hostPID: true`）。
- 挂 hostPath：
  - `/var/run/docker.sock` 或 `/run/containerd/containerd.sock`（拿 Pod 的 netns）
  - `/var/run/netns`
  - `/var/run/vntopo`（与 init 容器通信的 unix socket 目录）
  - 必要的 capabilities：`NET_ADMIN`、`SYS_ADMIN`、`NET_RAW`

主要职责：

1. **watch 本节点的 VNode**：通过 `fieldSelector=spec.nodeName=<self>` 只关心本节点 Pod 对应的 VNode。
2. **gRPC server**：暴露 unix socket，接受 init 容器的 `Setup` 请求（兼容 p2pnet 现有协议）。
3. **链路 diff**：
   - 取 `spec.links`（desired）
   - 扫描宿主机 / pod netns 实际设备（actual）
   - 对每条 link：判断 same-host vs cross-host，调对应建立函数
4. **不写 spec**，只写 `status.linkStatus[*]` 报告本端实际情况。
5. **drift detect**：60 秒一次扫描，清孤儿设备，补缺失。
6. **删除处理**：
   - 通过 K8s pod-delete 事件 + watcher 拿到 Pod 删除信号
   - 清理本节点对应的 `vh_*` / `vx_*` / `br_*` 设备

### 2.3 vntopo-init（initContainer）

- 由 controller 注入到每个 VNode Pod 的 `spec.initContainers`。
- 镜像基于 `p2pnet/client` 改造。
- 启动时通过本节点 unix socket（`/var/run/vntopo/agent.sock`）调 agent 的 `Setup` gRPC，告知 agent："我是 pod xxx 在 namespace yyy，请给我建链路"。
- 阻塞等待 agent 完成（带超时），退出 0 表示成功，非 0 让 Pod 进入 CrashLoopBackOff。

> 之所以保留 init 容器（而不是只靠 agent watch CR），是因为 watch 收到事件到 agent 反应可能有几百毫秒延迟，业务容器可能在链路就绪前就启动并尝试 ping 邻居导致初始化失败。init 容器是同步阻塞，确保业务容器启动时网络已就绪。

## 3. 控制循环（Reconciliation）

### 3.1 Controller Reconcile

```
Reconcile(VNode v):
  // 删除流程
  if v.deletionTimestamp:
    if has finalizer:
      for link in v.spec.links:
        peer = get(v.namespace, link.peer_pod)
        if peer: patch_remove_link(peer, uid=link.uid)
      delete_pod_if_exists(v)
      if pod still exists: requeue(2s); return
      remove_finalizer(v)
    return
  // 加 finalizer
  if not has finalizer: add_finalizer(v); requeue; return
  // 校验
  if !validate(v): set_phase(Failed); return
  // ensure Pod
  pod = get_pod(v.namespace, v.name)
  if not pod:
    create_pod(render_pod_from_template(v))
    set_phase(Creating); requeue(2s); return
  // 漂移检测
  if pod.containerID != v.status.containerID:
    mark_all_links_pending(v)  // agent 会重建
  // 分配 VNI
  for link in v.spec.links:
    peer = get(v.namespace, link.peer_pod)
    if peer && hostIP(pod) != peer.status.hostIP:
      ensure_vni_allocated(v, link.uid)  // 写 status.linkStatus[uid].vni
  // 同步 status
  v.status.observedGeneration = v.metadata.generation
  v.status.hostIP / hostNode / containerID = ...
  v.status.phase = derive_phase(pod, linkStatus)
  update_status(v)
  // 触发邻居（hostIP 变化时）
  if hostIP_changed: enqueue_peers(v)
  requeue(30s)  // 周期保险
```

### 3.2 Agent Reconcile

```
on VNode event (本节点 only) or on init Setup() call:
  v = get(VNode)
  desired = v.spec.links
  actual  = scan_local_devices(pod_netns(v))
  for link in desired:
    peer = get(v.namespace, link.peer_pod)
    if not peer || not peer.status.hostIP: skip(标 Pending); continue
    same_host = (peer.status.hostNode == self_node)
    if link not in actual:
      if same_host:
        make_veth(v, peer, link)         // 复用 koko
      else:
        make_vxlan(v, peer, link, vni)   // VNI 从 status 拿
      patch_status(v, link.uid, Established, mode)
  for orphan in (actual - desired):
    delete_link(orphan)
```

## 4. 数据平面具体实现

### 4.1 Same-host（A.host == B.host）

```
ip link add A_intf type veth peer name B_intf
ip link set A_intf netns $netnsA
ip link set B_intf netns $netnsB
ip netns exec $netnsA  ip addr add A.local_ip dev A_intf
ip netns exec $netnsA  ip link set A_intf up
ip netns exec $netnsB  ip addr add B.local_ip dev B_intf
ip netns exec $netnsB  ip link set B_intf up
```

只在 uid 较小的一端节点上做（避免双端竞争创建）；另一端 agent reconcile 时检测到设备已存在，标 Established。

### 4.2 Cross-host（A on N1, B on N2）

每端 agent 独立做对称的一半，**幂等**：

```
N1 上 agent:
  VNI = vni_from_status(uid)
  # 1. veth pair：pod 端 + host 端
  ip link add A_intf type veth peer name vh_<uid>
  ip link set A_intf netns $netnsA
  ip netns exec $netnsA ip addr add A.local_ip dev A_intf
  ip netns exec $netnsA ip link set A_intf up
  ip link set vh_<uid> up
  # 2. vxlan
  ip link add vx_<uid> type vxlan id $VNI \
       dstport 4789 \
       remote $N2_underlay_ip \
       local  $N1_underlay_ip \
       dev    $UNDERLAY_IFACE \
       nolearning
  ip link set vx_<uid> mtu $((NODE_MTU - 50))
  ip link set vx_<uid> up
  # 3. bridge 连接 host-side veth + vxlan
  ip link add br_<uid> type bridge
  ip link set vh_<uid> master br_<uid>
  ip link set vx_<uid> master br_<uid>
  ip link set br_<uid> up
```

N2 上 agent 镜像对称做。两端 VNI 必须一致，由 controller 写到 `status.linkStatus[uid].vni`，两端从同一份 status 读。

**对端节点 IP 怎么拿？**
- 通过 `peer.status.hostNode` → 查 `Node.status.addresses[InternalIP]`
- 缓存到自己的 `status.linkStatus[uid].underlayIP`

**Underlay 接口？**
- agent DaemonSet 通过 env 配置：`VNTOPO_UNDERLAY_IFACE=eth0`
- 默认值：解析 default route 出接口

### 4.3 删除

```
ip netns exec $netnsA ip link del A_intf 2>/dev/null  # veth 一端删，另一端自动删
ip link del br_<uid>  2>/dev/null                     # cross-host 才有
ip link del vx_<uid>  2>/dev/null                     # cross-host 才有
```

### 4.4 并发控制

`sync.Map[string]*sync.Mutex`，key = `<namespace>/<uid>`。所有对单条 link 的 make/delete 都先 Lock，保证幂等。

## 5. VNI 分配

```go
// VNI 24-bit 空间，碰撞概率极低
func ComputeVNI(namespace string, uid int64) uint32 {
    h := fnv.New32a()
    h.Write([]byte(namespace))
    h.Write([]byte(":"))
    h.Write([]byte(strconv.FormatInt(uid, 10)))
    return h.Sum32() & 0x00FFFFFF
}
```

冲突检测在 controller `ensure_vni_allocated` 里：
- 维护 namespace 内已分配 VNI set
- hash 命中已存在则线性 +1 探测

## 6. 命名规约

| 设备 | 命名 | 长度限制 | 说明 |
|---|---|---|---|
| Pod 内接口 | `<podA>_<podB>` | ≤15 | 沿用现有方案，前端保证 pod 名足够短 |
| Host-side veth | `vh<uid>` | ≤9 | uid 是 int64，实际不超 |
| VXLAN 设备 | `vx<uid>` | ≤9 | |
| Bridge 设备 | `br<uid>` | ≤9 | |

工具函数集中在 `internal/common/naming.go`。

## 7. 双向链路一致性

**v1：严格策略**

- reconcile 时检查 `peer.spec.links` 是否有对称 uid 条目
- 缺失 → `phase=Degraded`，`linkStatus[uid].state=Error: peer link missing`
- 不主动 patch 邻居（多对象事务复杂）

**v2：自愈**

- uid 较小的一端权威，缺失时 controller 自动 patch 大 uid 端

## 8. 删除联动

```
delete VNode v
  ↓
controller (finalizer 触发):
  1. patch 所有 peer 的 spec.links 移除指向 v 的 entry
       → 触发 peer reconcile
       → peer 节点的 agent 拆除自己一端的 vh/br/vx
  2. delete Pod (本来 ownerRef GC 也会，显式做更可控)
  3. 等 Pod 真消失（防止 agent 还没清完）
  4. 移除 finalizer → VNode 被真正删除
```

## 9. 漂移自愈

agent 每 60s 扫一次：

- 列出本节点应有的 link（基于 VNode CR）
- 列出宿主机/pod netns 实际的 vh*/vx*/br* 和 pod 内部 intf
- diff：
  - 多余设备（孤儿）→ 清掉
  - 缺失设备 → 补上
- 最后写 status

防止节点重启 / 手工误删 / kubelet 重建 pod 等场景下链路丢失。

## 10. 观测性

- **Prometheus metrics**：
  - `vntopo_controller_reconcile_total{result}`
  - `vntopo_controller_reconcile_duration_seconds`
  - `vntopo_agent_link_total{mode,state}`（mode=veth/vxlan，state=Established/Error）
  - `vntopo_agent_link_setup_duration_seconds{mode}`
- **K8s Events**：每次链路建立 / 失败 / 删除 emit Event 到 VNode 对象。
- **结构化日志**：zap，字段化输出 vnode/peer/uid/mode。
