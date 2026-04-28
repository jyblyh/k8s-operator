/*
Copyright 2026 BUPT AIOps Lab.
*/

// vntopo-init：作为 initContainer 注入到每个 VNode Pod。
//
// 行为：
//  1. 从环境变量读取 POD_NAME / POD_NAMESPACE / HOST_IP（由 controller 注入）。
//  2. 通过本节点 unix socket（默认 /var/run/vntopo/agent.sock，由 hostPath 挂载）
//     拨号到 vntopo-agent 并调用 SetupLinks。
//  3. 阻塞等待 agent 完成；成功 exit 0，失败 exit 1（让 Pod 进入 CrashLoopBackOff，
//     业务容器不会被启动）。
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/jyblyh/k8s-operator/internal/common"
	"github.com/jyblyh/k8s-operator/internal/initclient"
)

func main() {
	socketPath := flag.String("socket-path", common.AgentSocketPath, "agent unix socket")
	timeoutSec := flag.Int("timeout", 60, "max seconds to wait for agent SetupLinks")
	flag.Parse()

	podName := os.Getenv("POD_NAME")
	podNs := os.Getenv("POD_NAMESPACE")
	hostIP := os.Getenv("HOST_IP")
	if podName == "" || podNs == "" {
		fail("POD_NAME / POD_NAMESPACE env not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(
		ctx,
		"unix://"+*socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			d := net.Dialer{}
			path := strings.TrimPrefix(addr, "unix://")
			return d.DialContext(ctx, "unix", path)
		}),
	)
	if err != nil {
		fail("dial agent failed: %v", err)
	}
	defer conn.Close()

	if err := initclient.SetupLinks(ctx, conn, podNs, podName, hostIP); err != nil {
		fail("SetupLinks failed: %v", err)
	}

	os.Exit(0)
}

func fail(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "[vntopo-init] FATAL "+format+"\n", args...)
	os.Exit(1)
}
