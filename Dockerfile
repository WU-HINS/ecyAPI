# syntax=docker/dockerfile:1
# ---------- 构建阶段 ----------
# 原生架构构建：CI 在 amd64 / arm64 原生 runner 上各自编译（buildx --platform
# 与 runner 架构一致），不做 QEMU 跨架构模拟，产物与本机 go build 完全一致。
# libwebp 由 apk 提供（含静态库），配合 -tags webp_system 链接，无需仓库内的
# linux_amd64 预编译库，因此同一份 Dockerfile 可同时产出 amd64 / arm64。
FROM golang:1.27-alpine AS builder
RUN apk add --no-cache build-base libwebp-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=1 go build -tags webp_system -trimpath \
      -ldflags="-s -w -extldflags '-static'" -o /out/ecyapi ./cmd/server

# ---------- 运行阶段 ----------
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/ecyapi /app/ecyapi
# 容器直接以 root 运行：挂载宿主空目录/数据卷均无需处理属主权限，
# 数据目录缺失时由应用启动时自动创建
RUN mkdir -p /app/data/categories
ENV ECY_ADDR=:8080 ECY_DATA_DIR=/app/data/categories
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
ENTRYPOINT ["/app/ecyapi"]
