/*
Copyright 2026 BUPT AIOps Lab.
*/

package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// SetupTask 是 worker 处理的任务单元，由 RPC server / reconciler 入队。
//
// 任务粒度 = 单个 Pod。每个任务里 worker 会自己列 vn.spec.links 然后逐条建链。
type SetupTask struct {
	Namespace string
	PodName   string
	HostIP    string

	// EnqueuedAt 用于观测延迟：worker 拿到任务时记下 time.Since(EnqueuedAt)。
	EnqueuedAt time.Time
}

// SetupHandler 真正干活的回调：拿到一个任务，建好这个 Pod 的所有同节点 link，
// 写回 status，然后返回。出错时返回 error，由 worker 决定是否 requeue。
type SetupHandler func(ctx context.Context, task SetupTask) error

// WorkerPool 是固定大小的 goroutine 池 + 一个有界 channel。
//
// 设计目标：
//   - 解耦 RPC 请求与实际网络配置：RPC 立即返回 queued，net 操作慢就慢吧
//   - 限制并发：同节点上 netlink 操作太并发反而容易触发 Linux 内核竞争
//   - 失败重试：worker 内部退避重试 N 次，再不行写 status.lastError
type WorkerPool struct {
	tasks   chan SetupTask
	handler SetupHandler

	maxRetry  int           // 单任务最多重试几次
	retryWait time.Duration // 重试间隔（指数退避基线）

	wg     sync.WaitGroup
	stopCh chan struct{}
	closed bool
	mu     sync.Mutex
}

// NewWorkerPool 创建一个池但不启动。size = 同时处理的任务数；queue = 排队上限。
func NewWorkerPool(size, queue int, handler SetupHandler) *WorkerPool {
	if size <= 0 {
		size = 4
	}
	if queue <= 0 {
		queue = 256
	}
	return &WorkerPool{
		tasks:     make(chan SetupTask, queue),
		handler:   handler,
		maxRetry:  3,
		retryWait: 2 * time.Second,
		stopCh:    make(chan struct{}),
	}
}

// Start 启动 size 个 worker goroutine。
func (p *WorkerPool) Start(ctx context.Context, size int) {
	for i := 0; i < size; i++ {
		p.wg.Add(1)
		go p.run(ctx, i)
	}
}

// Enqueue 把任务塞进 queue。queue 满时立即返回错误（让 RPC 把 error 报给 init）。
//
// 这里**不阻塞**——RPC 调用要快进快出。如果 queue 真的满了，说明 agent 严重过载，
// 让 init 容器 CrashLoopBackOff 比假成功更安全。
func (p *WorkerPool) Enqueue(task SetupTask) error {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return errors.New("worker pool stopped")
	}

	if task.EnqueuedAt.IsZero() {
		task.EnqueuedAt = time.Now()
	}
	select {
	case p.tasks <- task:
		return nil
	default:
		return fmt.Errorf("worker queue full (cap=%d)", cap(p.tasks))
	}
}

// Stop 关闭池：拒绝新任务，等已入队的任务做完。
func (p *WorkerPool) Stop() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	close(p.stopCh)
	close(p.tasks)
	p.mu.Unlock()
	p.wg.Wait()
}

// run 单个 worker 的主循环。
func (p *WorkerPool) run(ctx context.Context, id int) {
	defer p.wg.Done()
	logger := log.Log.WithName("worker").WithValues("worker_id", id)

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stopCh:
			return
		case task, ok := <-p.tasks:
			if !ok {
				return
			}
			p.process(ctx, logger, task)
		}
	}
}

// process 处理单个任务，含退避重试。
func (p *WorkerPool) process(ctx context.Context, logger logr.Logger, task SetupTask) {
	queueLatency := time.Since(task.EnqueuedAt)

	var lastErr error
	for attempt := 0; attempt <= p.maxRetry; attempt++ {
		// 任务执行 ctx 单独控制超时，避免 worker 被一个慢任务卡死。
		taskCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := p.handler(taskCtx, task)
		cancel()

		if err == nil {
			logger.Info("setup done",
				"namespace", task.Namespace, "pod", task.PodName,
				"queue_latency_ms", queueLatency.Milliseconds(),
				"attempts", attempt+1)
			return
		}
		lastErr = err

		// ctx 取消就别重试了
		if ctx.Err() != nil {
			break
		}
		if attempt == p.maxRetry {
			break
		}

		// 指数退避，2s -> 4s -> 8s
		wait := p.retryWait * (1 << attempt)
		logger.Info("setup attempt failed, will retry",
			"namespace", task.Namespace, "pod", task.PodName,
			"attempt", attempt+1, "next_wait", wait, "err", err.Error())
		select {
		case <-ctx.Done():
			return
		case <-p.stopCh:
			return
		case <-time.After(wait):
		}
	}

	logger.Error(lastErr, "setup giving up",
		"namespace", task.Namespace, "pod", task.PodName,
		"max_retry", p.maxRetry)
}
