/*
Copyright 2026 BUPT AIOps Lab.
*/

// Package initclient 是 vntopo-init 调用本节点 vntopo-agent 的 jsonrpc 客户端。
//
// 协议参见 internal/netservice/types.go：
//   - 拨 unix socket（/var/run/vntopo/agent.sock）
//   - 调 NetService.SetupLinks(SetupReq) 返回 SetupResp
//   - status=queued / done → init 成功 exit 0
//   - status=error          → init 失败 exit 1
package initclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/rpc"
	"net/rpc/jsonrpc"
	"time"

	"github.com/jyblyh/k8s-operator/internal/netservice"
)

// Options 控制客户端行为。
type Options struct {
	// SocketPath unix socket 路径，默认 /var/run/vntopo/agent.sock。
	SocketPath string

	// DialTimeout 单次拨号超时；总等待时间 = DialTimeout × DialAttempts。
	DialTimeout time.Duration

	// DialAttempts 拨号最多重试次数。Pod 起来时 agent 可能还没 listen 好，
	// 这里给一个短期重试窗口。
	DialAttempts int

	// CallTimeout 单次 RPC 调用超时（含网络往返 + agent 处理时间）。
	// 我们的协议是 enqueue 即返回，agent 端处理应在毫秒级。
	CallTimeout time.Duration
}

// DefaultOptions 给 init 容器用的默认值。
func DefaultOptions(socketPath string) Options {
	return Options{
		SocketPath:   socketPath,
		DialTimeout:  3 * time.Second,
		DialAttempts: 10,
		CallTimeout:  5 * time.Second,
	}
}

// SetupLinks 是 init 容器对 agent 的核心调用：拨 socket → Call → 解析响应。
//
// 返回的 error 仅表示**通信层失败**或**协议层 error**（agent 显式拒绝）。
// 业务层"建链失败"不会通过这里上报——它会被异步写到 VNode.status.linkStatus。
func SetupLinks(ctx context.Context, opt Options, req netservice.SetupReq) (*netservice.SetupResp, error) {
	if opt.SocketPath == "" {
		return nil, errors.New("socket path empty")
	}

	conn, err := dialWithRetry(ctx, opt)
	if err != nil {
		return nil, fmt.Errorf("dial agent: %w", err)
	}
	defer conn.Close()

	cli := rpc.NewClientWithCodec(jsonrpc.NewClientCodec(conn))
	defer cli.Close()

	// 用一个独立 channel 等 Call 返回；ctx 到期时立即 unblock 上层。
	// （cli.Go 是异步 API；用它就避免了 cli.Call 的同步阻塞。）
	callCtx, cancel := context.WithTimeout(ctx, opt.CallTimeout)
	defer cancel()

	resp := &netservice.SetupResp{}
	done := cli.Go(netservice.MethodSetupLinks, req, resp, nil)

	select {
	case <-callCtx.Done():
		return nil, fmt.Errorf("rpc call timeout: %w", callCtx.Err())
	case call := <-done.Done:
		if call.Error != nil {
			return nil, fmt.Errorf("rpc call: %w", call.Error)
		}
	}

	return resp, nil
}

// dialWithRetry 在给定窗口内重试拨号；agent 启动慢或 DaemonSet 还在滚动时
// init 容器可能比 agent 早 1–2 秒，给一个短窗口可以避免假性失败。
func dialWithRetry(ctx context.Context, opt Options) (net.Conn, error) {
	attempts := opt.DialAttempts
	if attempts <= 0 {
		attempts = 1
	}
	timeout := opt.DialTimeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	var lastErr error
	for i := 0; i < attempts; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		d := net.Dialer{Timeout: timeout}
		dialCtx, cancel := context.WithTimeout(ctx, timeout)
		conn, err := d.DialContext(dialCtx, "unix", opt.SocketPath)
		cancel()
		if err == nil {
			return conn, nil
		}
		lastErr = err

		// 拨不通：等一小会儿重试
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil, lastErr
}
