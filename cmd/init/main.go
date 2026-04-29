/*
Copyright 2026 BUPT AIOps Lab.
*/

// vntopo-init：作为 initContainer 注入到每个 VNode Pod，向本节点 vntopo-agent
// 提交一次 SetupLinks 请求，通知 agent 把本 Pod 的所有同节点 link 建好。
//
// 行为
// ----
//  1. 从 Downward API 注入的环境变量读取 POD_NAME / POD_NAMESPACE / HOST_IP
//  2. 拨本节点 unix socket（hostPath 挂载的 /var/run/vntopo/agent.sock）
//  3. 调用 NetService.SetupLinks
//  4. 看响应 status：
//     · queued / done → 成功 exit 0，业务容器开始启动
//     · error          → exit 1，让 Pod CrashLoopBackOff（避免业务容器在错误的
//     网络配置下起来）
//
// 故意不阻塞等待 agent 真正完成建链：在 K8s init 阶段长时间阻塞会触发 kubelet
// 的启动超时，反而让排障变难。链路真正状态由 VNode.status.linkStatus 上报。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jyblyh/k8s-operator/internal/common"
	"github.com/jyblyh/k8s-operator/internal/initclient"
	"github.com/jyblyh/k8s-operator/internal/netservice"
)

func main() {
	socketPath := flag.String("socket-path", common.AgentSocketPath, "agent unix socket")
	totalTimeoutSec := flag.Int("timeout", 30, "overall timeout in seconds (dial + retry + call)")
	flag.Parse()

	podName := os.Getenv("POD_NAME")
	podNs := os.Getenv("POD_NAMESPACE")
	hostIP := os.Getenv("HOST_IP")
	if podName == "" || podNs == "" {
		fail("POD_NAME / POD_NAMESPACE env not set")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(*totalTimeoutSec)*time.Second,
	)
	defer cancel()

	resp, err := initclient.SetupLinks(
		ctx,
		initclient.DefaultOptions(*socketPath),
		netservice.SetupReq{
			Namespace: podNs,
			PodName:   podName,
			HostIP:    hostIP,
		},
	)
	if err != nil {
		fail("SetupLinks RPC failed: %v", err)
	}

	switch resp.Status {
	case netservice.StatusQueued, netservice.StatusDone:
		fmt.Fprintf(os.Stderr, "[vntopo-init] OK status=%s pod=%s/%s host=%s msg=%q\n",
			resp.Status, podNs, podName, hostIP, resp.Message)
		os.Exit(0)
	case netservice.StatusError:
		fail("agent returned error: %s", resp.Message)
	default:
		fail("unexpected response status: %s", resp.Status)
	}
}

func fail(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "[vntopo-init] FATAL "+format+"\n", args...)
	os.Exit(1)
}
