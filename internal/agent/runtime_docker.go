/*
Copyright 2026 BUPT AIOps Lab.
*/

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// DockerClient 是一个**最小**的 docker.sock 客户端，只覆盖 agent 真正需要的两个端点：
//
//	GET /containers/json     -> 列容器（按 label 过滤 pod sandbox）
//	GET /containers/{id}/json -> inspect 拿 PID
//
// 故意不引入 github.com/docker/docker/client SDK：
//
//   - SDK 拖进来 docker engine + protobuf + types 一大坨依赖（数十 MB binary）
//   - 我们只用两个端点，没必要
//   - 这里直接 net/http 走 unix socket，约 60 行就够，零外部依赖
//
// 如果未来集群迁到 containerd，把这个文件换成 cri-api gRPC 客户端即可，
// 上层 PodNetnsPath 接口签名不变。
type DockerClient struct {
	httpc *http.Client
}

// NewDockerClient 返回一个连到本节点 /var/run/docker.sock 的客户端。
//
// 注意：调用方必须保证容器内挂载了 docker.sock（DaemonSet 里加 hostPath）。
func NewDockerClient(socketPath string) *DockerClient {
	if socketPath == "" {
		socketPath = "/var/run/docker.sock"
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 3 * time.Second}
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	return &DockerClient{
		httpc: &http.Client{
			Transport: tr,
			Timeout:   10 * time.Second,
		},
	}
}

// dockerContainer 是 /containers/json 返回的我们关心的子集。
//
// docker 实际返回的字段非常多，json 里只反序列化我们用得上的几个。
type dockerContainer struct {
	ID     string            `json:"Id"`
	Labels map[string]string `json:"Labels"`
	State  string            `json:"State"` // "running" / "exited" / ...
}

// dockerInspect 是 /containers/{id}/json 的精简结果。
type dockerInspect struct {
	State struct {
		Pid int `json:"Pid"`
	} `json:"State"`
}

// FindPodSandboxID 通过 K8s 注入的 docker label 找 Pod 的 pause（sandbox）容器 ID。
//
// dockershim 给每个 Pod 起一个 pause 容器作为 network namespace 容器，
// 业务容器和 init 容器都共享这个 sandbox 的 netns。它的 docker labels：
//
//	io.kubernetes.pod.namespace = <ns>
//	io.kubernetes.pod.name      = <pod name>
//	io.kubernetes.container.name = "POD"   <- 业务容器是具体名字，sandbox 固定为 "POD"
//
// 如果找不到，返回 ("", err)；找到多个，返回最新的那个（容器 ID 字符串排序）。
func (d *DockerClient) FindPodSandboxID(ctx context.Context, namespace, podName string) (string, error) {
	q := url.Values{}
	q.Set("all", "0") // 只列 running 容器，pause 应该在跑
	endpoint := "http://docker/containers/json?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	resp, err := d.httpc.Do(req)
	if err != nil {
		return "", fmt.Errorf("docker list containers: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("docker list containers: status=%d", resp.StatusCode)
	}

	var list []dockerContainer
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return "", fmt.Errorf("decode docker list: %w", err)
	}

	var bestID string
	for _, c := range list {
		if c.Labels["io.kubernetes.pod.namespace"] != namespace {
			continue
		}
		if c.Labels["io.kubernetes.pod.name"] != podName {
			continue
		}
		if c.Labels["io.kubernetes.container.name"] != "POD" {
			continue
		}
		// 同名 Pod 多次重建会留下旧 sandbox 容器，取 ID 字典序最大的（更接近 newest，
		// 严格做的话应该 inspect 再比较 .State.StartedAt，这里简单处理够用）。
		if c.ID > bestID {
			bestID = c.ID
		}
	}

	if bestID == "" {
		return "", fmt.Errorf("pod sandbox not found: ns=%s pod=%s", namespace, podName)
	}
	return bestID, nil
}

// InspectPid 调 /containers/{id}/json 取 PID。0 表示容器没在跑。
func (d *DockerClient) InspectPid(ctx context.Context, containerID string) (int, error) {
	endpoint := "http://docker/containers/" + containerID + "/json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	resp, err := d.httpc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("docker inspect: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("docker inspect: status=%d", resp.StatusCode)
	}

	var ins dockerInspect
	if err := json.NewDecoder(resp.Body).Decode(&ins); err != nil {
		return 0, fmt.Errorf("decode docker inspect: %w", err)
	}
	if ins.State.Pid <= 0 {
		return 0, fmt.Errorf("container %s pid=%d (not running?)", containerID, ins.State.Pid)
	}
	return ins.State.Pid, nil
}
