//go:build cgo && webp_system

// 系统 libwebp 构建：链接顺序由 CGO_LDFLAGS / pkg-config 提供。
// CI 与 Dockerfile 都先安装 libwebp-dev（含头文件与静态库）再使用本标签。
package webp

/*
#cgo LDFLAGS: -lwebp -lm
*/
import "C"
