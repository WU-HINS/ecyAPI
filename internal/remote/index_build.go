package remote

// index_build.go —— M3：把远端目录树扫成「紧凑索引」（只存元数据，不下载任何原图）。
//
// 产物与本地 index.bin 同格式，只是分类都带 index.FlagRemote；
// 服务端后续用它来服务 /@web/*（原图按需代理，不落盘）。

import (
	"context"
	"fmt"
	"log"
	"path"
	"sort"
	"strings"

	"ecyapi/internal/index"
)

// IndexOptions 建索引参数。
type IndexOptions struct {
	MaxDepth   int      // 与服务端 ECY_MAX_DEPTH 一致
	Extensions []string // 只收这些后缀（空 = 全收）
	Logger     *log.Logger
}

// BuildIndexFromSource 递归扫描远端，生成紧凑索引文件。
// 返回写入的条目数。
func BuildIndexFromSource(ctx context.Context, src Source, dst string, opt IndexOptions) (int, error) {
	if opt.MaxDepth <= 0 {
		opt.MaxDepth = 3
	}
	if opt.Logger == nil {
		opt.Logger = log.Default()
	}
	// 云端默认只索引图片：视频经代理不可用（云盘限速），索引里留着只会产生 403。
	if len(opt.Extensions) == 0 {
		opt.Extensions = []string{".png", ".jpg", ".jpeg", ".webp", ".gif", ".avif", ".bmp", ".jxl"}
	}

	builder := &index.Builder{}
	total := 0
	dirs := 0

	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		entries, err := src.List(ctx, dir)
		if err != nil {
			return err
		}
		cb := index.CatBuild{Name: dir, Flags: index.FlagRemote}
		for _, e := range entries {
			if e.IsDir {
				if depth+1 < opt.MaxDepth {
					if err := walk(e.Path, depth+1); err != nil {
						return err
					}
				}
				continue
			}
			if !wantExt(e.Path, opt.Extensions) {
				continue
			}
			cb.Items = append(cb.Items, index.Entry{
				Name:  path.Base(e.Path),
				Size:  clampSize(e.Size),
				MTime: int64(e.ModTime.Unix()),
				Flags: uint8(index.OriUnknown),
			})
		}
		if len(cb.Items) > 0 && cb.Name != "" { // 根目录不作为分类
			builder.AddCategory(cb)
			total += len(cb.Items)
			dirs++
			opt.Logger.Printf("  分类 %s：%d 个文件", cb.Name, len(cb.Items))
		}
		return nil
	}
	if err := walk("", 0); err != nil {
		return 0, err
	}
	if total == 0 {
		return 0, fmt.Errorf("远端没有任何可索引的图片")
	}
	if err := builder.Build(dst); err != nil {
		return 0, err
	}
	opt.Logger.Printf("云端索引完成：%d 个分类 / %d 条 → %s（未下载任何原图）", dirs, total, dst)
	return total, nil
}

func clampSize(v int64) int64 {
	if v < 0 {
		return 0
	}
	if v > 1<<32-1 {
		return 1<<32 - 1
	}
	return v
}

var _ = sort.Strings
var _ = strings.TrimSpace
