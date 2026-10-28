//go:build cgo && webp_system && webp_sharpyuv

// 系统 libwebp（sharpyuv 拆分包）：libwebp ≥ 1.3 把 sharpyuv 拆成独立静态库，
// 静态链接时必须额外带上 sharpyuv。Ubuntu 24.04 / Debian 13 用这个标签，
// 并安装 libwebp-dev + libsharpyuv-dev。
package webp

/*
#cgo LDFLAGS: -lwebp -lsharpyuv -lm
*/
import "C"
