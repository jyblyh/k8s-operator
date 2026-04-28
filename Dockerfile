# Dockerfile：vntopo-controller 镜像
# 多阶段构建，最终产物是 distroless 静态二进制。

FROM golang:1.22 AS builder
WORKDIR /workspace

COPY go.mod go.sum* ./
RUN go mod download || true

COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/
COPY hack/ hack/

ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN go build -trimpath -ldflags "-s -w" -o /out/vntopo-controller ./cmd/controller

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /out/vntopo-controller /vntopo-controller
USER 65532:65532
ENTRYPOINT ["/vntopo-controller"]
