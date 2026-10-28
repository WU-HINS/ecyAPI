//go:build cgo && linux && amd64 && !webp_system

// 默认构建：链接仓库内置的 linux/amd64 静态库，无需系统安装 libwebp。
// 其他架构请使用 "-tags webp_system"。
package webp

/*
#cgo LDFLAGS: ${SRCDIR}/lib/linux_amd64/libwebp.a -lm
*/
import "C"
