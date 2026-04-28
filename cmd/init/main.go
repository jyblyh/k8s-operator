/*
Copyright 2026 BUPT AIOps Lab.
*/

// vntopo-init：作为 initContainer 注入到每个 VNode Pod。
//
// 阶段说明
// =========
//
// **M1（当前）**：完全 no-op。只打印 Downward API 注入的环境变量，立即 exit 0，
// 让业务容器能起来。我们这一阶段验证的是 controller 的 ensurePod + syncStatus，
// 跟 agent 没关系；如果在这里去拨 agent socket，反而会被
// "agent 那边 gRPC server 暂时还没注册任何 Service" 这种非业务问题卡住。
//
// **M2**：恢复真正的 gRPC 调用：
//
//	conn, err := grpc.DialContext(ctx, "unix://"+socketPath, ...)
//	cli       := netservicepb.NewLocalClient(conn)
//	resp, err := cli.SetupLinks(ctx, &netservicepb.SetupReq{ ... })
//
// 此时 agent 已经注册了 LocalServer，初始化失败就让 Pod CrashLoopBackOff，
// 是合理的——业务容器拿不到对端，本来也不该起。
package main

import (
	"fmt"
	"os"
)

func main() {
	podName := os.Getenv("POD_NAME")
	podNs := os.Getenv("POD_NAMESPACE")
	hostIP := os.Getenv("HOST_IP")
	if podName == "" || podNs == "" {
		fmt.Fprintln(os.Stderr, "[vntopo-init] FATAL POD_NAME / POD_NAMESPACE env not set")
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr,
		"[vntopo-init] M1 no-op: pod=%s/%s host=%s — link setup will be done in M2\n",
		podNs, podName, hostIP)
	os.Exit(0)
}
