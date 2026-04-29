/*
Copyright 2026 BUPT AIOps Lab.
*/

package agent

import (
	"context"
	"fmt"
	"os"
)

// PodNetns 提供 (namespace, podName) -> Linux 网络命名空间路径 的能力。
//
// 实现方式（当前 Docker shim 集群）：
//
//  1. 通过 DockerClient.FindPodSandboxID 找到 pod 的 pause 容器
//  2. InspectPid 拿到这个容器的进程 PID
//  3. 拼出 /proc/<pid>/ns/net 作为 netns 文件路径
//
// 这个路径是一个 nsfs inode，open 它得到 fd 后可以 setns(2) 进入。
// netlink 库的 netns.GetFromPath / LinkSetNsFd 都接受这种路径。
//
// 未来集群升级到 containerd 后，把 docker 调用换成 cri-api 即可，
// 接口签名不变。
type PodNetns struct {
	docker *DockerClient

	// HostProcPrefix 默认 /proc。当 agent 容器把宿主 /proc 挂载到 /host/proc 时，
	// 这里改成 "/host/proc" 即可定位到宿主 PID 视角下的 ns 文件。
	HostProcPrefix string
}

// NewPodNetns 用给定 docker 客户端构造一个 PodNetns 解析器。
//
// hostProcPrefix 为空时默认 "/proc"——只要 agent 是 hostPID:true，自身 /proc
// 就是宿主 PID 命名空间，能直接看到所有容器进程。
func NewPodNetns(docker *DockerClient, hostProcPrefix string) *PodNetns {
	if hostProcPrefix == "" {
		hostProcPrefix = "/proc"
	}
	return &PodNetns{
		docker:         docker,
		HostProcPrefix: hostProcPrefix,
	}
}

// LookupPath 返回 /proc/<pid>/ns/net 形式的文件路径。
//
// 副作用：函数会 stat 一下路径确认文件存在，避免上层 netlink 操作时才报
// 难定位的"file not found"。
func (p *PodNetns) LookupPath(ctx context.Context, namespace, podName string) (string, error) {
	if p == nil || p.docker == nil {
		return "", fmt.Errorf("PodNetns not initialized")
	}

	sandboxID, err := p.docker.FindPodSandboxID(ctx, namespace, podName)
	if err != nil {
		return "", fmt.Errorf("find sandbox: %w", err)
	}
	pid, err := p.docker.InspectPid(ctx, sandboxID)
	if err != nil {
		return "", fmt.Errorf("inspect sandbox %s: %w", sandboxID, err)
	}

	path := fmt.Sprintf("%s/%d/ns/net", p.HostProcPrefix, pid)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("stat netns %s: %w", path, err)
	}
	return path, nil
}
