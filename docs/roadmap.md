# 里程碑路线图

## M0：脚手架（3 天）

- [x] 设计文档定稿（README / architecture / crd-spec / roadmap）
- [x] CRD YAML 草稿 + Go types 草稿
- [ ] `kubebuilder init` 生成项目骨架
- [ ] `kubebuilder create api --group vntopo --version v1alpha1 --kind VNode`
- [ ] 把 `vnode_types.go` 草稿合并进 kubebuilder 生成的版本
- [ ] `make generate manifests` 跑通
- [ ] Dockerfile（controller / agent / init）
- [ ] Makefile：`docker-build` / `docker-push` / `deploy` / `undeploy`
- [ ] CI：lint / unit test / image build

**验收**：`make deploy` 能在 kind 集群起 controller + agent，`kubectl get crd vnodes.vntopo.bupt.site` 看到 CRD。

## M1：单节点 veth 闭环（1.5 周）

- [ ] controller：VNode → Pod 创建 / 删除（含 ownerRef + finalizer）
- [ ] controller：admission webhook 基础校验
- [ ] controller：status 聚合（phase / observedGeneration / hostIP / containerID）
- [ ] agent：从 fork 的 meshnet-cni daemon 切出 veth 部分（复用 koko）
- [ ] agent：watch 本节点 VNode + 链路 diff
- [ ] init 容器：从 p2pnet/client 改造，调本节点 agent 的 unix socket
- [ ] examples：single-host-veth.yaml
- [ ] 单测：reconciler 关键分支

**验收**：用现有前端的 testbed（一个 DC 内全部设备）走完部署流程，所有 pod Running，`host1 → asw1 → csw1` 路径 ping 通。

## M2：跨节点 VXLAN（1.5 周）

- [ ] controller：VNI 分配（namespace 内冲突检测）
- [ ] controller：cross-host link 状态判定（pod hostIP 不同 → vxlan）
- [ ] agent：vxlan 设备 + bridge 创建（`internal/agent/netlink_vxlan.go`）
- [ ] agent：从 K8s Node 资源拿对端 underlay IP
- [ ] agent：MTU 自适应（NODE_MTU - 50）
- [ ] examples：cross-host-vxlan.yaml
- [ ] e2e：两节点 kind 集群，r1 in node1, r2 in node2

**验收**：把 r1 强制调度到 node1，r2 调度到 node2，`r1 → r2` 直连 ping 通；跨 DC `host1@dc1 → host1@dc2` 路径全通。

## M3：稳健性（1 周）

- [ ] agent：drift detect（60s 周期扫描 + 自愈）
- [ ] controller：Pod 重建时（containerID 变化）所有 linkStatus 标 Pending
- [ ] controller：邻居反向触发（hostIP 变化 enqueue 所有 peer）
- [ ] controller：删除联动（patch 邻居 → 等 Pod 消失 → 移除 finalizer）
- [ ] 故障注入测试：
  - [ ] 手工 `kubectl delete pod` 后链路自动恢复
  - [ ] `kubectl drain` 节点后跨节点链路在 pod 重建后恢复
  - [ ] 手工 `ip link del vh_xxx` 后 60s 内被 agent 补回
- [ ] webhook：dataCenter 一致性强制校验

**验收**：上述故障注入测试全部通过；`kubectl wait --for=condition=LinksConverged vnode/host1 --timeout=2m` 可用。

## M4：前端 / 后端切换（1 周）

- [ ] microops-fe：去掉 Pod / ConfigMap / Topology YAML 生成逻辑，只生成 VNode CR
- [ ] microops-fe：DC nodeSelector 选择框
- [ ] aiops-evaluation：`routes/testbed.py` 改成 apply VNode CR
- [ ] 老的 p2pnet 标记为 deprecated（保留代码不删，方便回滚）
- [ ] 文档：迁移指南

**验收**：在前端走完一次完整流程：画拓扑 → 选 DC 节点 → 部署 → 弹性增加 pod → 拆除。

## M5：可观测 + e2e（1 周）

- [ ] Prometheus exporter（controller / agent）
- [ ] K8s Events 完整化
- [ ] kind-based e2e 测试套件
- [ ] envtest 单测覆盖率 ≥60%
- [ ] 用户文档 / 运维手册

**验收**：CI 跑通 e2e；新人按文档可以独立部署一遍。

## v1 之外（v2 候选）

- 双向链路自愈（controller 自动 patch 缺失对端）
- 单独 Link CRD（强对称语义，重构）
- 链路指标实时探测（ping_exporter 集成）
- 网络策略 / NetworkPolicy 联动
- 支持 OVS-DPDK / SR-IOV 等高性能数据面
