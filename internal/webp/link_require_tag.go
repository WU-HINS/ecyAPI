//go:build cgo && !webp_system && !(linux && amd64)

package webp

// 仓库只内置了 linux/amd64 的 libwebp 静态库。其他平台/架构请在系统安装
// libwebp 开发包后加 "-tags webp_system" 构建；或设置 CGO_ENABLED=0
// （此时 ?w= 压缩预览回退原图）。
//
// 这里故意引用一个不存在的标识符，让编译直接报出可操作的原因，
// 而不是一串 undefined reference to 'WebPEncode'。
var _ = webp_system_tag_required_outside_linux_amd64
