package remote

// probe.go —— 只取文件头解出图片宽高（不落盘、不拉整图）。

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

// ProbeSteps 逐级放大的取头大小：多数图片 8KB 足够，
// 带大 EXIF/ICC 的 JPEG 需要 64KB。
var ProbeSteps = []int64{8192, 65536}

// ProbeDimensions 用 Range 取文件头，解出宽高。
//
// 返回 (w, h, format, 实际读到的字节, error)。全过程只在内存里放一个 ≤64KB 的缓冲，
// 不写磁盘、不下载整图。
func ProbeDimensions(ctx context.Context, rng RangeReader, rel string) (int, int, string, int, error) {
	var lastErr error
	for _, n := range ProbeSteps {
		rc, err := rng.OpenRange(ctx, rel, n)
		if err != nil {
			return 0, 0, "", 0, err
		}
		buf := make([]byte, n)
		read, _ := readFull(rc, buf)
		_ = rc.Close()
		cfg, format, err := image.DecodeConfig(bytes.NewReader(buf[:read]))
		if err == nil && cfg.Width > 0 && cfg.Height > 0 {
			return cfg.Width, cfg.Height, format, read, nil
		}
		lastErr = err
		if int64(read) < n { // 文件比请求还短，再放大也没用
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("无法识别的图片头")
	}
	return 0, 0, "", 0, lastErr
}

// RangeReader 是 OpenRange 的最小接口（WebDAV 与 S3 都满足）。
type RangeReader interface {
	OpenRange(ctx context.Context, rel string, n int64) (io.ReadCloser, error)
}

func readFull(rc io.ReadCloser, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := rc.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
		if m == 0 {
			return n, nil
		}
	}
	return n, nil
}
