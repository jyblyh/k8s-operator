/*
Copyright 2026 BUPT AIOps Lab.
*/

package agent

import "fmt"

// PodNetnsPath 通过 sandbox 容器 ID 推导出 Pod 的 netns 文件路径。
//
// 实现策略（M1）：调用 CRI 接口或者读 containerd/docker 的 sandbox 信息：
//
//	containerd: /run/containerd/io.containerd.grpc.v1.cri/sandboxes/<id>/netns
//	docker:     /proc/<pid>/ns/net （pid 来自 docker inspect）
//
// 也可以直接 `crictl inspect <containerID>` 解析。
//
// p2pnet 现有方案是从 docker.sock + dockerclient 拿 PID，再用 /proc/<pid>/ns/net。
// fork meshnet-cni 后建议改用 containerd CRI，更通用。
func PodNetnsPath(containerID string) (string, error) {
	if containerID == "" {
		return "", fmt.Errorf("empty containerID")
	}
	// TODO(M1): 接入 CRI / containerd 客户端
	return "", fmt.Errorf("not implemented")
}
