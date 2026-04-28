# netservice 协议

`vntopo-init` initContainer 与 `vntopo-agent` DaemonSet 之间的本地 gRPC 协议。
故意保持与 `p2pnet` 现有协议结构相近，方便 fork 时复用客户端。

## 生成 Go 代码

```bash
protoc --go_out=. --go_opt=paths=source_relative \
       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
       internal/netservice/netservice.proto
```

或者通过 Makefile：

```bash
make proto
```

生成产物（`netservice.pb.go` / `netservice_grpc.pb.go`）需要提交到仓库。
