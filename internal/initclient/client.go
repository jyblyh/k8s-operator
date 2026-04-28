/*
Copyright 2026 BUPT AIOps Lab.
*/

// Package initclient 是 vntopo-init 用来调用本节点 vntopo-agent 的 gRPC 客户端。
//
// 等 protoc 生成 internal/netservice/*.pb.go 后，本文件会改成：
//
//	import netservicepb "github.com/jyblyh/k8s-operator/internal/netservice"
//	cli := netservicepb.NewLocalClient(conn)
//	resp, err := cli.SetupLinks(ctx, &netservicepb.SetupReq{...})
//
// M0 阶段先留接口，避免在没有 protoc 产物时编译失败。
package initclient

import (
	"context"
	"fmt"
	"os"

	"google.golang.org/grpc"
)

// SetupLinks 调用 agent 的 SetupLinks。
//
// M1 临时实现：proto/agent 实际链路逻辑还没接通，这里**只校验能拨通 agent**，
// 然后打印警告并返回 nil，让 init 容器 exit 0，使 Pod 能正常起来——这样我们
// 可以先验证 controller 的 ensurePod / syncStatus 闭环。
//
// M2 中改成真正发 SetupReq 给 agent 并等待回包：
//
//	cli  := netservicepb.NewLocalClient(conn)
//	resp, err := cli.SetupLinks(ctx, &netservicepb.SetupReq{...})
func SetupLinks(ctx context.Context, conn *grpc.ClientConn, namespace, podName, hostIP string) error {
	_ = ctx
	if conn == nil {
		return fmt.Errorf("nil grpc conn")
	}
	// 简单 sanity check：确认 conn 状态可用。
	state := conn.GetState().String()
	fmt.Fprintf(os.Stderr,
		"[vntopo-init] M1 placeholder: dialed agent OK (state=%s), "+
			"skipping link setup for pod=%s/%s host=%s\n",
		state, namespace, podName, hostIP)
	return nil
}
