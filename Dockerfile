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
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/ecyapi /app/ecyapi
# 容器直接以 root 运行：挂载宿主空目录/数据卷均无需处理属主权限，
# 数据目录缺失时由应用启动时自动创建
RUN mkdir -p /app/data/categories
ENV ECY_ADDR=:8080 ECY_DATA_DIR=/app/data/categories
EXPOSE 8080
ENTRYPOINT ["/app/ecyapi"]
