/*
Copyright 2026 BUPT AIOps Lab.
*/

package agent

import (
	"context"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	vntopov1alpha1 "github.com/jyblyh/k8s-operator/api/v1alpha1"
)

// DriftScanner 周期性 enqueue 本节点上每个 VNode，让 SetupHandler 重做一遍。
//
// 这是一道兜底防线：常态触发路径有两条（init 容器 RPC + reconciler watch），
// 但都假设 K8s API 状态变更**才会**触发同步。如果发生：
//
//   - 用户手动 `ip link del` 删掉了某条 link（pod 没重建，VNode 没变）
//   - 容器内 IP 配置被踩掉
//   - 节点重启后短暂期间链路丢失
//
// 仅靠 watch 是发现不了的——必须周期性主动巡检。SetupHandler 自带幂等：
// 已建好的 link 直接 skip，没建的会建，所以 drift scan 是"无害的重新校准"。
//
// 频率：默认 60s 一次（common.AgentDriftScanSec）。
type DriftScanner struct {
	Reader   client.Reader // 直查 apiserver；不要走 cache 避免假性数据
	NodeName string
	Pool     *WorkerPool
	Interval time.Duration
}

// Run 阻塞运行，直到 ctx 取消。建议放到独立 goroutine。
func (d *DriftScanner) Run(ctx context.Context) {
	if d.Interval <= 0 {
		d.Interval = 60 * time.Second
	}
	logger := log.FromContext(ctx).WithName("drift-scan").WithValues("node", d.NodeName)

	t := time.NewTicker(d.Interval)
	defer t.Stop()

	logger.Info("drift scanner started", "interval", d.Interval)
	for {
		select {
		case <-ctx.Done():
			logger.Info("drift scanner stopped")
			return
		case <-t.C:
			d.scanOnce(ctx)
		}
	}
}

// scanOnce 列出本节点 VNode，逐个 enqueue。worker 内部去重，不会重复跑。
func (d *DriftScanner) scanOnce(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("drift-scan")
	var list vntopov1alpha1.VNodeList
	if err := d.Reader.List(ctx, &list); err != nil {
		logger.Error(err, "list vnodes failed")
		return
	}
	enqueued := 0
	skipped := 0
	for i := range list.Items {
		vn := &list.Items[i]
		if !d.belongsToThisNode(vn) {
			skipped++
			continue
		}
		// 软入队：worker queue 满 / 重复入队都不视作错误，下个周期再来一次。
		if err := d.Pool.Enqueue(SetupTask{
			Namespace:  vn.Namespace,
			PodName:    vn.Name,
			EnqueuedAt: time.Now(),
		}); err != nil {
			logger.V(1).Info("enqueue dropped", "vn", vn.Name, "err", err)
			continue
		}
		enqueued++
	}
	logger.V(1).Info("scan tick", "enqueued", enqueued, "skipped", skipped)
}

// belongsToThisNode 与 SetupHandler 中同名方法语义一致。
func (d *DriftScanner) belongsToThisNode(vn *vntopov1alpha1.VNode) bool {
	if vn.Status.HostNode == d.NodeName {
		return true
	}
	if vn.Spec.NodeSelector != nil {
		if v, ok := vn.Spec.NodeSelector["kubernetes.io/hostname"]; ok && v == d.NodeName {
			return true
		}
	}
	return false
}
