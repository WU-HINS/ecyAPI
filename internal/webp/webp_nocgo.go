//go:build !cgo

package webp

import (
	"fmt"
	"image"
)

// Options 编码参数（无 CGO 时保持同样的 API 形态）。
type Options struct {
	Quality float32
}

// EncodeRGBA 在 CGO_ENABLED=0 时不可用：返回错误，调用方（预览链路）
// 自动回退原图，接口行为不受影响，只是没有 ?w= 压缩。
func EncodeRGBA(img *image.RGBA, o Options) ([]byte, error) {
	return nil, fmt.Errorf("webp: 未启用 CGO，压缩预览不可用（请用 CGO_ENABLED=1 构建，或安装 libwebp 后加 -tags webp_system）")
}
