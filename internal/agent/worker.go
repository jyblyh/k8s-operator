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

// SetupTaskFunc 真正干活的回调：拿到一个任务，建好这个 Pod 的所有同节点 link，
// 写回 status，然后返回。出错时返回 error，由 worker 决定是否 requeue。
//
// 注意：本类型故意不叫 SetupHandler——同包里 setup_handler.go 已经有
// `type SetupHandler struct` 表示具体实现对象；这里要的是它的方法签名抽象，
// 改名避免类型重名冲突。实例化时传 handler.Handle 即可。
type SetupTaskFunc func(ctx context.Context, task SetupTask) error

// WorkerPool 是固定大小的 goroutine 池 + 一个有界 channel。
//
// 设计目标：
//   - 解耦 RPC 请求与实际网络配置：RPC 立即返回 queued，net 操作慢就慢吧
//   - 限制并发：同节点上 netlink 操作太并发反而容易触发 Linux 内核竞争
//   - 失败重试：worker 内部退避重试 N 次，再不行写 status.lastError
type WorkerPool struct {
	tasks   chan SetupTask
	handler SetupTaskFunc

	maxRetry  int           // 单任务最多重试几次
	retryWait time.Duration // 重试间隔（指数退避基线）

	// pending 跟踪当前**还在 channel 等待 worker 接走**的任务 key（"ns/name"）。
	// reconciler / RPC 在短时间内对同一 Pod 多次 enqueue 时，重复请求会被合并成
	// 一次任务——避免 status patch 引发 reconcile → re-enqueue → status patch
	// 的反馈环把队列爆掉。
	//
	// 一旦 worker 把任务从 channel 取走，就从 pending 删掉；后续如果有新事件
	// 来（比如对端 Pod 重建），还会被正常 enqueue 一次，确保不丢失变更。
	pending sync.Map

	wg     sync.WaitGroup
	stopCh chan struct{}
	closed bool
	mu     sync.Mutex
}

// NewWorkerPool 创建一个池但不启动。size = 同时处理的任务数；queue = 排队上限。
func NewWorkerPool(size, queue int, handler SetupTaskFunc) *WorkerPool {
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

// Enqueue 把任务塞进 queue。
//
//   - **dedup**：同一个 (ns, name) 已经在队列里等待时直接 noop（视作成功），
//     避免 reconciler / RPC 高频触发把队列灌爆。
//   - **不阻塞**：channel 满时立即返回错误，让上游决定怎么处理（RPC 报错给
//     init，reconciler 下次 reconcile 再试）。
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

	key := task.Namespace + "/" + task.PodName
	// 同 key 已经在队列里等待 → 直接合并成功
	if _, loaded := p.pending.LoadOrStore(key, struct{}{}); loaded {
		return nil
	}

	select {
	case p.tasks <- task:
		return nil
	default:
		// 没塞进去，回滚 pending 标记，让下次 enqueue 还能尝试
		p.pending.Delete(key)
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
			// 任务被 worker 接走了：从 pending 删掉，让后续新事件还能正常入队。
			// 注意要在 process 前删——process 里 patch status 又会触发 reconcile
			// 重新 enqueue，那次合理的 enqueue 不应该被这次的 pending 标记挡住。
			p.pending.Delete(task.Namespace + "/" + task.PodName)
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
