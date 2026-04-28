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

	"google.golang.org/grpc"
)

// SetupLinks 调用 agent 的 SetupLinks。
//
// M0 占位实现：直接返回 not-implemented，让运行时显式提示尚未接通；
// 实际生效在 M1 把 proto 生成产物加上之后。
func SetupLinks(ctx context.Context, conn *grpc.ClientConn, namespace, podName, hostIP string) error {
	_ = conn
	_ = ctx
	_ = namespace
	_ = podName
	_ = hostIP
	return fmt.Errorf("initclient.SetupLinks: not implemented yet (waiting for M1 proto codegen)")
}
