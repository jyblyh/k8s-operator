/*
Copyright 2026 BUPT AIOps Lab.
*/

// vntopo-agent：每个 K8s worker 节点跑一份的 DaemonSet 数据平面。
//
// 启动后做的事：
//  1. 读取本节点身份（NODE_NAME 环境变量，由 DaemonSet 注入 fieldRef）。
//  2. 启动 controller-runtime manager，watch 本节点上有 Pod 的 VNode。
//  3. 启动一个 unix socket gRPC server，接受 init 容器的 Setup 请求。
//  4. 启动周期 drift 扫描（M3）。
package main

import (
	"flag"
	"net"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	vntopov1alpha1 "github.com/bupt-aiops/vntopo-operator/api/v1alpha1"
	"github.com/bupt-aiops/vntopo-operator/internal/agent"
	"github.com/bupt-aiops/vntopo-operator/internal/common"
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
		underlayIface  string
		nodeNameEnvKey string
	)
	flag.StringVar(&socketPath, "socket-path", common.AgentSocketPath,
		"Unix socket path that init containers use to call this agent.")
	flag.StringVar(&underlayIface, "underlay-iface", "",
		"Underlay interface name used by VXLAN devices. Empty => auto-detect default route.")
	flag.StringVar(&nodeNameEnvKey, "node-name-env", "NODE_NAME",
		"Env var that holds the kubernetes node name this agent runs on.")

	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	nodeName := os.Getenv(nodeNameEnvKey)
	if nodeName == "" {
		setupLog.Error(nil, "node name env not set", "env", nodeNameEnvKey)
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		// Agent 不需要 leader election：每个节点都要跑。
		LeaderElection: false,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	r := &agent.Reconciler{
		Client:        mgr.GetClient(),
		NodeName:      nodeName,
		UnderlayIface: underlayIface,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to register reconciler")
		os.Exit(1)
	}

	// gRPC unix socket server（被 init 容器调用）
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
	go func() {
		if err := agent.RunGRPCServer(lis, r); err != nil {
			setupLog.Error(err, "grpc server exited")
			os.Exit(1)
		}
	}()

	setupLog.Info("starting agent manager", "node", nodeName)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "manager exited with error")
		os.Exit(1)
	}
}
