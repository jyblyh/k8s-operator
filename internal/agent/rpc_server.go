/*
Copyright 2026 BUPT AIOps Lab.
*/

package agent

import (
	"errors"
	"fmt"
	"net"
	"net/rpc"
	"net/rpc/jsonrpc"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/jyblyh/k8s-operator/internal/netservice"
)

// NetService 是注册到 net/rpc 的服务对象，承载 init 容器调用。
//
// 字段全部不可变；并发安全靠 enqueue 通道 + worker pool 自身保证。
type NetService struct {
	enqueue func(SetupTask) error // 由 main 注入：把任务推到 worker queue
}

// NewNetService 构造 NetService。enqueue 由调用方传入（通常是 worker pool 的入口）。
func NewNetService(enqueue func(SetupTask) error) *NetService {
	return &NetService{enqueue: enqueue}
}

// SetupLinks 是 net/rpc 暴露的方法。签名必须满足：
//
//	func (T) MethodName(argType T1, replyType *T2) error
//
// 这样 net/rpc 才会自动注册它。
func (s *NetService) SetupLinks(req netservice.SetupReq, resp *netservice.SetupResp) error {
	if req.Namespace == "" || req.PodName == "" {
		resp.Status = netservice.StatusError
		resp.Message = "namespace / pod_name required"
		return nil
	}

	task := SetupTask{
		Namespace: req.Namespace,
		PodName:   req.PodName,
		HostIP:    req.HostIP,
	}
	if err := s.enqueue(task); err != nil {
		resp.Status = netservice.StatusError
		resp.Message = err.Error()
		return nil
	}

	resp.Status = netservice.StatusQueued
	resp.Message = fmt.Sprintf("setup task queued for %s/%s", req.Namespace, req.PodName)
	return nil
}

// RunRPCServer 在给定 listener 上跑 jsonrpc server，每个连接一个 goroutine。
//
// 协议格式：每个连接一个 jsonrpc 会话；net/rpc 的 jsonrpc.ServeConn 默认按
// "请求-响应-请求-响应"的方式串行处理，不做 multiplexing。init 容器只调用
// 一次 SetupLinks 然后断开，所以这种简单模型够用。
//
// stopCh 用来关闭整个 server：close(stopCh) 后 listener 关掉，
// Accept 返回错误然后 RunRPCServer 退出；已建立的连接会读到 EOF 自然结束。
func RunRPCServer(lis net.Listener, svc *NetService, stopCh <-chan struct{}) error {
	logger := log.Log.WithName("rpc-server")

	rpcSrv := rpc.NewServer()
	if err := rpcSrv.RegisterName(netservice.ServiceName, svc); err != nil {
		return fmt.Errorf("register rpc service: %w", err)
	}

	// 关闭通道与 listener 的联动。
	go func() {
		<-stopCh
		_ = lis.Close()
	}()

	var wg sync.WaitGroup
	for {
		conn, err := lis.Accept()
		if err != nil {
			// 用 stopCh 判断是不是被主动关停。stopCh 已 close 时 accept 错误属于正常。
			select {
			case <-stopCh:
				wg.Wait()
				return nil
			default:
			}
			// 临时网络错误：继续 accept。
			if errors.Is(err, net.ErrClosed) {
				wg.Wait()
				return nil
			}
			logger.Error(err, "rpc accept failed, will continue")
			continue
		}

		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			defer c.Close()
			rpcSrv.ServeCodec(jsonrpc.NewServerCodec(c))
		}(conn)
	}
}

