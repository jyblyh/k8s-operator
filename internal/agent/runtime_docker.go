/*
Copyright 2026 BUPT AIOps Lab.
*/

package agent

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// DockerClient 是一个**最小**的 docker.sock 客户端，只覆盖 agent 真正需要的端点：
//
//	GET  /containers/json              -> 列容器（按 label 过滤 pod sandbox / 业务容器）
//	GET  /containers/{id}/json         -> inspect 拿 PID
//	POST /containers/{id}/exec         -> 创建 exec 实例（M4 ConfigMap reload）
//	POST /exec/{id}/start              -> 启动 exec 并读 stdout/stderr
//	GET  /exec/{id}/json               -> 拿 ExitCode
//
// 故意不引入 github.com/docker/docker/client SDK：
//
//   - SDK 拖进来 docker engine + protobuf + types 一大坨依赖（数十 MB binary）
//   - 我们只用上面这几个端点，没必要
//   - 这里直接 net/http 走 unix socket，零外部依赖
//
// 如果未来集群迁到 containerd，把这个文件换成 cri-api gRPC 客户端即可。
type DockerClient struct {
	// httpc 用于普通短请求（list / inspect），10s 超时已足够
	httpc *http.Client
	// execHttpc 没有 Timeout，长连接由 ctx 控制；exec stream 不能被 client.Timeout 截断
	execHttpc *http.Client
}

// NewDockerClient 返回一个连到本节点 /var/run/docker.sock 的客户端。
//
// 注意：调用方必须保证容器内挂载了 docker.sock（DaemonSet 里加 hostPath）。
func NewDockerClient(socketPath string) *DockerClient {
	if socketPath == "" {
		socketPath = "/var/run/docker.sock"
	}
	mkTr := func() *http.Transport {
		return &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: 3 * time.Second}
				return d.DialContext(ctx, "unix", socketPath)
			},
			// 连接复用对短请求够用；exec 长流单独走 execHttpc，避免污染连接池。
			DisableKeepAlives: false,
			IdleConnTimeout:   30 * time.Second,
		}
	}
	return &DockerClient{
		httpc: &http.Client{
			Transport: mkTr(),
			Timeout:   10 * time.Second,
		},
		execHttpc: &http.Client{
			Transport: mkTr(),
			// 不设 Timeout：超时由 caller 通过 ctx 控制
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

// =============================================================================
//  Exec：在容器里执行命令（M4 ConfigMap reload 用）
// =============================================================================

// ExecResult 一次 docker exec 的结果。
type ExecResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// FindContainerID 找指定 Pod 内**业务容器**（不是 pause sandbox）的 docker ID。
//
// 与 FindPodSandboxID 的区别：filter 走 io.kubernetes.container.name != "POD"，
// 也就是用户 spec.template.containers[0] 对应的那个 docker 容器。
//
// 如果同 Pod 有多个业务容器（多容器 Pod），优先按 containerName 匹配；
// containerName 为空时取 main container（template.containers[0].name）。
//
// 多副本 Pod 不存在（VNode 1:1 Pod），所以这里不需要 generation 比较。
func (d *DockerClient) FindContainerID(ctx context.Context, namespace, podName, containerName string) (string, error) {
	q := url.Values{}
	q.Set("all", "0")
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

	var bestID, fallbackID string
	for _, c := range list {
		if c.Labels["io.kubernetes.pod.namespace"] != namespace {
			continue
		}
		if c.Labels["io.kubernetes.pod.name"] != podName {
			continue
		}
		cn := c.Labels["io.kubernetes.container.name"]
		if cn == "POD" {
			continue // 跳过 sandbox
		}
		// 任何业务容器都可作为 fallback
		if c.ID > fallbackID {
			fallbackID = c.ID
		}
		if containerName != "" && cn == containerName {
			if c.ID > bestID {
				bestID = c.ID
			}
		}
	}
	if bestID != "" {
		return bestID, nil
	}
	if fallbackID != "" {
		return fallbackID, nil
	}
	return "", fmt.Errorf("business container not found: ns=%s pod=%s container=%s",
		namespace, podName, containerName)
}

// Exec 在指定容器里执行 cmd，等命令结束并返回 stdout/stderr/exitcode。
//
// 实现选 Plain HTTP 而不是 Hijack 协议：
//   - 不发 Connection: Upgrade 头，docker daemon 用 chunked HTTP stream 返回输出
//   - response.Body 直接是 docker stream multiplex（8 字节 header + payload）
//   - net/http 自动处理 chunked 解码，我们只关心解 mux 帧
//
// docker mux 帧格式（来自 docker engine API v1.40 文档）：
//
//	[0]    StreamType: 0=stdin 1=stdout 2=stderr
//	[1-3]  reserved
//	[4-7]  payload size (big-endian uint32)
//	[8...] payload bytes
//
// Tty=false 是这个 mux 协议的前提；Tty=true 时 stdout/stderr 合并为单流，我们用不到。
//
// 取消 / 超时由 ctx 控制。
func (d *DockerClient) Exec(ctx context.Context, containerID string, cmd []string) (*ExecResult, error) {
	if containerID == "" {
		return nil, fmt.Errorf("Exec: empty containerID")
	}
	if len(cmd) == 0 {
		return nil, fmt.Errorf("Exec: empty cmd")
	}

	// ---------- 1. create exec ----------
	createBody := map[string]interface{}{
		"AttachStdout": true,
		"AttachStderr": true,
		"Tty":          false,
		"Cmd":          cmd,
	}
	createBuf, err := json.Marshal(createBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://docker/containers/"+containerID+"/exec",
		bytes.NewReader(createBuf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker exec create: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("docker exec create: status=%d body=%s",
			resp.StatusCode, string(body))
	}

	var execID struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&execID); err != nil {
		return nil, fmt.Errorf("decode exec create: %w", err)
	}
	if execID.ID == "" {
		return nil, fmt.Errorf("docker exec create: empty id")
	}

	// ---------- 2. start exec & read stream ----------
	startBody, err := json.Marshal(map[string]interface{}{
		"Detach": false,
		"Tty":    false,
	})
	if err != nil {
		return nil, err
	}
	req2, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://docker/exec/"+execID.ID+"/start",
		bytes.NewReader(startBody))
	if err != nil {
		return nil, err
	}
	req2.Header.Set("Content-Type", "application/json")

	resp2, err := d.execHttpc.Do(req2)
	if err != nil {
		return nil, fmt.Errorf("docker exec start: %w", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp2.Body, 4096))
		return nil, fmt.Errorf("docker exec start: status=%d body=%s",
			resp2.StatusCode, string(body))
	}

	var stdout, stderr bytes.Buffer
	if err := readDockerStream(resp2.Body, &stdout, &stderr); err != nil {
		return nil, fmt.Errorf("read exec stream: %w", err)
	}

	// ---------- 3. inspect exec for exit code ----------
	req3, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://docker/exec/"+execID.ID+"/json", nil)
	if err != nil {
		return nil, err
	}
	resp3, err := d.httpc.Do(req3)
	if err != nil {
		return nil, fmt.Errorf("docker exec inspect: %w", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker exec inspect: status=%d", resp3.StatusCode)
	}
	var inspectExec struct {
		ExitCode int  `json:"ExitCode"`
		Running  bool `json:"Running"`
	}
	if err := json.NewDecoder(resp3.Body).Decode(&inspectExec); err != nil {
		return nil, fmt.Errorf("decode exec inspect: %w", err)
	}
	if inspectExec.Running {
		// 极端情况：daemon 已 OK 关闭 stream 但状态尚未刷新；轮询一次足够。
		// 简单处理：直接报错让上层重试。
		return nil, fmt.Errorf("exec still running after stream EOF")
	}

	return &ExecResult{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		ExitCode: inspectExec.ExitCode,
	}, nil
}

// readDockerStream 解 docker exec 的 8-byte-header multiplex 流。
//
// 一直读到 EOF（命令结束 daemon 关流）；返回时 stdout/stderr 缓冲区已就绪。
//
// 上限保护：每条 mux 帧 size 字段是 32-bit，理论最大 4GB；不在这里限制总长度，
// 上层调用方传入的 buffer 足够小（reload 命令输出极短）。
func readDockerStream(r io.Reader, stdout, stderr io.Writer) error {
	var hdr [8]byte
	for {
		_, err := io.ReadFull(r, hdr[:])
		if err == io.EOF {
			return nil
		}
		if err == io.ErrUnexpectedEOF {
			// 流被中途切断，但已经收到部分数据；返回 nil 让上层用现有数据
			return nil
		}
		if err != nil {
			return err
		}
		streamType := hdr[0]
		size := binary.BigEndian.Uint32(hdr[4:8])
		var dst io.Writer
		switch streamType {
		case 1:
			dst = stdout
		case 2:
			dst = stderr
		default:
			dst = io.Discard
		}
		if size == 0 {
			continue
		}
		if _, err := io.CopyN(dst, r, int64(size)); err != nil {
			return err
		}
	}
}
