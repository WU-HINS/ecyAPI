# syntax=docker/dockerfile:1
# ---------- 构建阶段 ----------
FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ecyapi ./cmd/server

# ---------- 运行阶段 ----------
FROM alpine:3.20
# su-exec 供 entrypoint 初始化数据卷属主后降权到 ecy(10001) 运行
RUN adduser -D -u 10001 ecy \
    && apk add --no-cache ca-certificates tzdata \
    && apk add --no-cache --repository https://dl-cdn.alpinelinux.org/alpine/v3.20/community su-exec
WORKDIR /app
COPY --from=builder /out/ecyapi /app/ecyapi
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh \
    && mkdir -p /app/data/categories \
    && chown -R ecy:ecy /app
ENV ECY_ADDR=:8080 ECY_DATA_DIR=/app/data/categories
EXPOSE 8080
# 默认 root 启动：entrypoint 兼容 docker run -v / compose 挂载空目录场景，
# 自动把数据卷属主修正为 ecy(10001) 后用 su-exec 降权运行业务进程。
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
