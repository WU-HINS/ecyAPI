package remote

// probe_index.go —— 后台批量探测：只对「方向未知」的条目取 8KB 头，判向并回写索引。
//
// 特点：
//   - 不下载原图、不落盘（只在内存里放 8KB 头）
//   - 写入的是 mmap 索引里的 1 个字节（oriFlags bits0-1）
//   - 可中断（ctx 取消即停），已判定的条目不会重复探测
//   - 限速：固定 worker 数 + 每次探测后可选休眠

import (
	"context"
	"log"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ecyapi/internal/index"
)

// ProbeOptions 批量探测参数。
type ProbeOptions struct {
	Workers int           // 并发探测数（默认 4）
	Limit   int           // 最多探测多少条（0 = 全部未知）
	Delay   time.Duration // 每次探测后的休眠（限速用，0 = 不限）
	Logger  *log.Logger
}

// imageExt 只对图片后缀做探测。
func imageExt(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".avif", ".bmp", ".jxl", ".heic":
		return true
	}
	return false
}

// ProbeResult 探测统计。
type ProbeResult struct {
	Scanned   int // 遍历到的条目
	Unknown   int // 需要探测的
	Probed    int // 实际探测成功
	Failed    int // 失败（网络/解码）
	ReadBytes int64
}

// ProbeIndex 对索引里方向未知的条目做批量探测并回写。
func ProbeIndex(ctx context.Context, ix *index.Index, src RangeReader, opt ProbeOptions) ProbeResult {
	if opt.Workers <= 0 {
		opt.Workers = 4
	}
	if opt.Logger == nil {
		opt.Logger = log.Default()
	}
	var res ProbeResult

	type job struct {
		cat  index.Category
		pos  int
		name string
	}
	jobs := make(chan job, opt.Workers*2)
	var wg sync.WaitGroup
	var probed, failed atomic.Int64
	var readBytes atomic.Int64
	var total int64

	for i := 0; i < opt.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if ctx.Err() != nil {
					return
				}
				w, h, _, n, err := ProbeDimensions(ctx, src, j.cat.Name+"/"+j.name)
				if err != nil {
					if k := failed.Add(1); k <= 5 {
						opt.Logger.Printf("  探测失败 %s/%s: %v", j.cat.Name, j.name, err)
					}
					continue
				}
				readBytes.Add(int64(n))
				flags := uint8(0)
				switch {
				case h > w:
					flags = index.OriPort
				default:
					flags = index.OriLand // 横图与方图统一按横处理（与扫描口径一致）
				}
				if err := ix.SetFlags(j.cat, j.pos, flags); err == nil {
					probed.Add(1)
				} else {
					failed.Add(1)
				}
				if opt.Delay > 0 {
					time.Sleep(opt.Delay)
				}
			}
		}()
	}

	feed := func() {
		defer close(jobs)
		for _, cat := range ix.Categories() {
			for i := 0; i < cat.Count; i++ {
				if ctx.Err() != nil {
					return
				}
				rec, ok := ix.At(cat, i)
				if !ok {
					continue
				}
				res.Scanned++
				if rec.Flags&0x3 != index.OriUnknown {
					continue
				}
				// 非图片后缀直接跳过：视频/压缩包取头也解不出宽高，白费一次请求
				if !imageExt(rec.Name) {
					continue
				}
				res.Unknown++
				if opt.Limit > 0 && total >= int64(opt.Limit) {
					return
				}
				total++
				jobs <- job{cat: cat, pos: i, name: rec.Name}
				if res.Scanned%200 == 0 {
					opt.Logger.Printf("探测进度：已扫 %d 条，命中未知 %d，完成 %d",
						res.Scanned, res.Unknown, probed.Load())
				}
			}
		}
	}
	feed()
	wg.Wait()

	res.Probed = int(probed.Load())
	res.Failed = int(failed.Load())
	res.ReadBytes = readBytes.Load()
	return res
}
