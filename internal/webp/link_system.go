//go:build cgo && webp_system

// 系统 libwebp 构建：CI 与 Dockerfile 都先安装 libwebp-dev + libsharpyuv-dev
// （libwebp ≥ 1.3 把 sharpyuv 拆成独立静态库，静态链接时必须一并带上）。
package webp

/*
#cgo LDFLAGS: -lwebp -lsharpyuv -lm
*/
import "C"
