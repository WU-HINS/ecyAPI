# ecyAPI 常用构建命令
#
#   make build    编译静态二进制（需要 CGO + libwebp）
#   make test     跑测试
#   make assets   下载看板娘的大体积动画到 data/static（首次部署时执行一次）
#   make run      本地跑起来

GO ?= go
BIN ?= ecyapi
TAGS ?= webp_system

.PHONY: build test assets run fmt vet

build:
	CGO_ENABLED=1 $(GO) build -tags $(TAGS) -ldflags "-s -w" -o $(BIN) ./cmd/server

test:
	CGO_ENABLED=1 $(GO) test -tags $(TAGS) ./...

# 看板娘动画约 50 MB，不随仓库分发；下载到 ECY_STATIC_DIR（默认 data/static）。
assets:
	$(GO) run ./tools/fetchassets -dir data/static

run: build
	./$(BIN)

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...
