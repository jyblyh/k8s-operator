/*
Copyright 2026 BUPT AIOps Lab.
*/

// vntopo-agent：每个 K8s worker 节点跑一份的 DaemonSet 数据平面。
//
// 启动后做的事
// ============
//  1. 读取本节点身份（NODE_NAME 环境变量，由 DaemonSet 注入 fieldRef）
//  2. 启动 controller-runtime manager，watch 本节点上有 Pod 的 VNode
//  3. 启动 jsonrpc unix socket server，接受 init 容器的 SetupLinks 请求
//  4. 启动异步 WorkerPool：消费 SetupTask，调 SetupHandler 真正建链
//  5. 周期 drift 扫描（M3 中加入）
package main

import (
	"context"
	"flag"
	"net"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	vntopov1alpha1 "github.com/jyblyh/k8s-operator/api/v1alpha1"
	"github.com/jyblyh/k8s-operator/internal/agent"
	"github.com/jyblyh/k8s-operator/internal/common"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("agent-setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(vntopov1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		socketPath     string
		dockerSocket   string
		hostProcPrefix string
		underlayIface  string // M3 使用
		nodeNameEnvKey string
		workerSize     int
		workerQueueCap int
	)
	flag.StringVar(&socketPath, "socket-path", common.AgentSocketPath,
		"Unix socket path that init containers use to call this agent.")
	flag.StringVar(&dockerSocket, "docker-socket", "/var/run/docker.sock",
		"Path to docker.sock; agent uses it to find pod sandbox PID.")
	flag.StringVar(&hostProcPrefix, "host-proc", "/proc",
		"Host /proc prefix. Set to /host/proc when bind-mounted.")
	flag.StringVar(&underlayIface, "underlay-iface", "",
		"Underlay interface name used by VXLAN devices. Empty => auto-detect default route.")
	flag.StringVar(&nodeNameEnvKey, "node-name-env", "NODE_NAME",
		"Env var that holds the kubernetes node name this agent runs on.")
	flag.IntVar(&workerSize, "workers", 4, "Concurrent worker goroutines.")
	flag.IntVar(&workerQueueCap, "worker-queue", 256, "Max pending tasks queued.")

	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	nodeName := os.Getenv(nodeNameEnvKey)
	if nodeName == "" {
		setupLog.Error(nil, "node name env not set", "env", nodeNameEnvKey)
		os.Exit(1)
	}

	// ---- controller-runtime manager（agent 不做 leader election，每个节点都要跑）
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:         scheme,
		LeaderElection: false,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// ---- 数据平面：docker → netns → setup handler
	dockerCli := agent.NewDockerClient(dockerSocket)
	podNetns := agent.NewPodNetns(dockerCli, hostProcPrefix)

	handler := &agent.SetupHandler{
		Client:        mgr.GetClient(),
		Reader:        mgr.GetAPIReader(), // bypass cache，启动早期也能读到 VNode
		NodeName:      nodeName,
		Netns:         podNetns,
		Locks:         agent.NewLinkLocks(),
		UnderlayIface: underlayIface, // M3 才会真正用到
	}

	// ---- worker pool：RPC server / reconciler 共享同一个池
	pool := agent.NewWorkerPool(workerSize, workerQueueCap, handler.Handle)

	// ---- reconciler：watch VNode/Pod，把变化推到 pool
	r := &agent.Reconciler{
		Client:   mgr.GetClient(),
		NodeName: nodeName,
		Pool:     pool,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to register reconciler")
		os.Exit(1)
	}

	// ---- jsonrpc unix socket server（init 容器调它）
	if err := os.MkdirAll(common.AgentSocketDir, 0o755); err != nil {
		setupLog.Error(err, "mkdir socket dir failed")
		os.Exit(1)
	}
	_ = os.Remove(socketPath) // 启动时清掉旧 socket
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		setupLog.Error(err, "listen unix socket failed", "path", socketPath)
		os.Exit(1)
	}
	if err := os.Chmod(socketPath, 0o660); err != nil {
		setupLog.Error(err, "chmod socket failed", "path", socketPath)
		os.Exit(1)
	}
	netSvc := agent.NewNetService(pool.Enqueue)

	// ---- 启动顺序：先 worker，再 RPC server，最后 manager（manager 是 blocking call）
	rootCtx, cancelRoot := context.WithCancel(ctrl.SetupSignalHandler())
	defer cancelRoot()

	pool.Start(rootCtx, workerSize)
	defer pool.Stop()

	rpcStop := make(chan struct{})
	go func() {
		<-rootCtx.Done()
		close(rpcStop)
	}()
	go func() {
		if err := agent.RunRPCServer(lis, netSvc, rpcStop); err != nil {
			setupLog.Error(err, "rpc server exited with error")
		}
	}()

	setupLog.Info("starting agent manager",
		"node", nodeName, "socket", socketPath, "workers", workerSize,
		"docker", dockerSocket, "host_proc", hostProcPrefix)
	if err := mgr.Start(rootCtx); err != nil {
		setupLog.Error(err, "manager exited with error")
		os.Exit(1)
	}
}
