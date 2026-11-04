package remote

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestProbeDimensions 只取头解宽高：206 与「忽略 Range 的 200」两条路径都要对。
func TestProbeDimensions(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 300, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 300; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()

	for _, honorRange := range []bool{true, false} {
		honorRange := honorRange
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rng := r.Header.Get("Range")
			if honorRange && strings.HasPrefix(rng, "bytes=0-") {
				end, _ := strconv.Atoi(strings.TrimPrefix(rng, "bytes=0-"))
				if end >= len(data) {
					end = len(data) - 1
				}
				w.Header().Set("Content-Range", "bytes 0-"+strconv.Itoa(end)+"/"+strconv.Itoa(len(data)))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(data[:end+1])
				return
			}
			_, _ = w.Write(data) // 忽略 Range
		}))

		src, _ := NewWebDAV(srv.URL, "", "", "")
		w, h, format, n, err := ProbeDimensions(context.Background(), src, "x.jpg")
		srv.Close()
		if err != nil {
			t.Fatalf("honorRange=%v: %v", honorRange, err)
		}
		if w != 300 || h != 200 || format != "jpeg" {
			t.Fatalf("honorRange=%v: 得到 %dx%d %s", honorRange, w, h, format)
		}
		if honorRange && n > 8192 {
			t.Fatalf("206 分支读取超过 8KB：%d", n)
		}
		if !honorRange && n > 65536 {
			t.Fatalf("忽略 Range 时读超了：%d", n)
		}
		t.Logf("honorRange=%v → %dx%d %s，读 %d 字节（整图 %d）", honorRange, w, h, format, n, len(data))
	}
}
