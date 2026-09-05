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
RUN adduser -D -u 10001 ecy && apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/ecyapi /app/ecyapi
RUN mkdir -p /app/data/categories && chown -R ecy:ecy /app
USER ecy
ENV ECY_ADDR=:8080 ECY_DATA_DIR=/app/data/categories
EXPOSE 8080
ENTRYPOINT ["/app/ecyapi"]
