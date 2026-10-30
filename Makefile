# ecyAPI 常用构建命令
#
#   make build    编译静态二进制（需要 CGO + libwebp）
#   make test     跑测试
#   make assets   下载看板娘的大体积动画到 data/static（首次部署时执行一次）
#   make run      本地跑起来

GO ?= go
BIN ?= ecyapi
# 默认用仓库内置的静态 libwebp（internal/webp/lib，仅 linux/amd64）。
# CI 与 Docker 镜像用系统库：make build TAGS="webp_system webp_sharpyuv"
TAGS ?=

.PHONY: build test assets dist run fmt vet

build:
	CGO_ENABLED=1 $(GO) build -tags $(TAGS) -ldflags "-s -w" -o $(BIN) ./cmd/server

test:
	CGO_ENABLED=1 $(GO) test -tags $(TAGS) ./...

# 看板娘动画约 50 MB，不随仓库分发；下载到 ECY_STATIC_DIR（默认 data/static）。
assets:
	$(GO) run ./tools/fetchassets -dir data/static

# 本地打一个「带素材的完整包」，产物与 Release 里的 tar.gz 结构一致。
#   dist/ecyapi-<version>-linux-<arch>/
#     ├── ecyapi            二进制
#     ├── data/static/      看板娘素材（默认 ECY_STATIC_DIR 指向这里）
#     └── README.md
VERSION ?= dev
ARCH ?= $(shell go env GOARCH)
dist:
	$(GO) run ./tools/fetchassets -dir static
	mkdir -p dist build/ecyapi-$(VERSION)-linux-$(ARCH)/data
	CGO_ENABLED=1 $(GO) build -tags "$(TAGS)" -trimpath \
		-ldflags "-s -w -extldflags '-static'" -o dist/ecyapi ./cmd/server
	cp dist/ecyapi build/ecyapi-$(VERSION)-linux-$(ARCH)/ecyapi
	cp -r static build/ecyapi-$(VERSION)-linux-$(ARCH)/data/static
	cp README.md build/ecyapi-$(VERSION)-linux-$(ARCH)/
	tar -C build -czf dist/ecyapi-$(VERSION)-linux-$(ARCH).tar.gz ecyapi-$(VERSION)-linux-$(ARCH)
	@echo "产物: dist/ecyapi-$(VERSION)-linux-$(ARCH).tar.gz"

run: build
	./$(BIN)

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...
