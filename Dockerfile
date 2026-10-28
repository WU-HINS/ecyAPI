# syntax=docker/dockerfile:1
# ---------- 构建阶段 ----------
# 原生架构构建：CI 在 amd64 / arm64 原生 runner 上各自编译（buildx --platform
# 与 runner 架构一致），不做 QEMU 跨架构模拟，产物与本机 go build 一致。
# libwebp 使用发行版静态库（libwebp-dev + libsharpyuv-dev），因此同一份
# Dockerfile 可同时产出 amd64 / arm64 的完全静态二进制。
FROM golang:1.27-bookworm AS builder
RUN apt-get update && apt-get install -y --no-install-recommends \
      libwebp-dev libsharpyuv-dev \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=1 go build -tags webp_system -trimpath \
      -ldflags="-s -w -extldflags '-static'" -o /out/ecyapi ./cmd/server

# ---------- 运行阶段 ----------
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates tzdata curl \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /out/ecyapi /app/ecyapi
# 容器直接以 root 运行：挂载宿主空目录/数据卷均无需处理属主权限，
# 数据目录缺失时由应用启动时自动创建
RUN mkdir -p /app/data/categories
ENV ECY_ADDR=:8080 ECY_DATA_DIR=/app/data/categories
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD curl -fsS http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/app/ecyapi"]
