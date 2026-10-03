// Package webp 提供 WebP 有损编码，基于静态编译的 libwebp（CGO）。
//
// 背景：?w= 压缩预览需要 webp 输出，此前用的 gen2brain/webp 在
// CGO_ENABLED=0 纯静态构建下回退 wasm2go 后端，每次编码都持有
// 数百 MB 不归还的 wasm 堆，批量压缩会打爆内存。这里直接静态链接
// libwebp（musl 交叉编译），内存与耗时都回到正常水平。
//
// 构建约束：需要 CGO_ENABLED=1。Dockerfile 已配置；本地裸 go build
// 在无交叉工具链时构建失败属预期，CI/镜像构建不受影响。
package webp

/*
#cgo CFLAGS: -I${SRCDIR}/include
#cgo linux,amd64 LDFLAGS: ${SRCDIR}/lib/linux_amd64/libwebp.a -lm
#include "bridge.h"
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"image"
	"unsafe"
)

// Options 编码参数。
type Options struct {
	Quality float32 // 1~100，0 取默认 82
}

// EncodeRGBA 把 RGBA 图像编码为有损 WebP。
func EncodeRGBA(img *image.RGBA, o Options) ([]byte, error) {
	quality := o.Quality
	if quality <= 0 || quality > 100 {
		quality = 82
	}
	b := img.Bounds()
	if b.Dx() < 1 || b.Dy() < 1 || len(img.Pix) == 0 {
		return nil, fmt.Errorf("webp: empty image")
	}
	var out *C.uint8_t
	var outSize C.size_t
	ok := C.webp_bridge_encode_rgba(
		(*C.uint8_t)(unsafe.Pointer(&img.Pix[0])),
		C.int(b.Dx()), C.int(b.Dy()), C.int(img.Stride),
		C.float(quality), &out, &outSize)
	if ok == 0 {
		return nil, fmt.Errorf("webp: encode failed")
	}
	defer C.free(unsafe.Pointer(out))
	return C.GoBytes(unsafe.Pointer(out), C.int(outSize)), nil
}
