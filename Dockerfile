# syntax=docker/dockerfile:1
# ---------- 构建阶段 ----------
# 原生架构构建：CI 在 amd64 / arm64 原生 runner 上各自编译（buildx --platform
# 与 runner 架构一致），不做 QEMU 跨架构模拟，产物与本机 go build 一致。
# libwebp 使用发行版静态库（bookworm 的 libwebp.a 已自带 sharpyuv），
# 因此同一份 Dockerfile 可同时产出 amd64 / arm64 的完全静态二进制。
FROM golang:1.27-bookworm AS builder
RUN apt-get update && apt-get install -y --no-install-recommends \
      libwebp-dev \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
# FETCH_ASSETS=1（默认）：构建上下文里没有素材时，用容器内的 Go 工具链自动下载一次，
# 这样直接 docker build . 出来的镜像就带看板娘动画，不用先跑 make assets。
# CI 每次推送的镜像传 --build-arg FETCH_ASSETS=0 跳过（构建更快）。
ARG FETCH_ASSETS=1
COPY go.mod go.sum ./
RUN go mod download
# 整份上下文拷进来：构建需要 internal/，还要把（可选的）static/ 静态资源带进镜像。
COPY . .
RUN mkdir -p /src/static && \
    if [ "$FETCH_ASSETS" = "1" ] && [ -z "$(ls -A /src/static/assets/webm 2>/dev/null)" ]; then \
      echo "上下文没有素材，开始下载到 /src/static ..." && \
      go run ./tools/fetchassets -dir /src/static || echo "素材下载失败：镜像将不含动画（可容器内用 ecyapi -fetch-assets 补）"; \
    fi
RUN CGO_ENABLED=1 go build -tags webp_system -trimpath \
      -ldflags="-s -w -extldflags '-static'" -o /out/ecyapi ./cmd/server

# ---------- 运行阶段 ----------
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates tzdata curl \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /out/ecyapi /app/ecyapi
# 看板娘素材的来源放在 /app/static（镜像层）；运行目录 /app/data 通常是数据卷，
# 挂载会盖住镜像里的同名目录，所以启动时把素材同步进 /app/data/static
# （静态资源同步见 internal/server/static.go：只补缺失/更新的文件）。
COPY --from=builder /src/static /app/static
RUN mkdir -p /app/data/static
ENV ECY_STATIC_DIR=/app/data/static
ENV ECY_STATIC_SYNC_FROM=/app/static
# 容器直接以 root 运行：挂载宿主空目录/数据卷均无需处理属主权限，
# 数据目录缺失时由应用启动时自动创建
RUN mkdir -p /app/data/categories
ENV ECY_ADDR=:8080 ECY_DATA_DIR=/app/data/categories
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD curl -fsS http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/app/ecyapi"]
