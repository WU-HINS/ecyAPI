//go:build cgo && webp_system && !webp_sharpyuv

// 系统 libwebp（自包含）：libwebp.a 内部已编译进 sharpyuv，
// 典型如 Debian 12 / Ubuntu 22.04 / 仓库内置的预编译库。
package webp

/*
#cgo LDFLAGS: -lwebp -lm
*/
import "C"
