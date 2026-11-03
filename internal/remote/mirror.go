package remote

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MirrorOptions 镜像参数。
type MirrorOptions struct {
	Dest       string   // 本地目标目录（= 服务端的 ECY_DATA_DIR）
	Workers    int      // 并发下载数
	MaxDepth   int      // 远端最大层级（与服务端 ECY_MAX_DEPTH 保持一致，多余层级不拉）
	Prune      bool     // 删除本地多余文件（远端已删）
	Extensions []string // 只同步这些后缀（为空表示原样同步）
	DryRun     bool     // 只打印计划，不落盘
	Logger     *log.Logger
}

// MirrorResult 一次同步的统计。
type MirrorResult struct {
	Scanned  int
	Added    int
	Updated  int
	Deleted  int
	Failed   int
	Bytes    int64
	Duration time.Duration
}

type job struct {
	entry Entry
}

// Mirror 把远端目录树增量同步到本地目录。
//
// 判断依据：本地缺失 → 下载；大小不同或远端 mtime 更新 → 重下。
// 下载走「临时文件 + rename」，中途失败不会留下半个文件被扫描器索引到。
func Mirror(ctx context.Context, src Source, opt MirrorOptions) (MirrorResult, error) {
	if opt.Workers <= 0 {
		opt.Workers = 4
	}
	if opt.MaxDepth <= 0 {
		opt.MaxDepth = 3
	}
	if opt.Logger == nil {
		opt.Logger = log.Default()
	}
	if err := os.MkdirAll(opt.Dest, 0o755); err != nil {
		return MirrorResult{}, err
	}

	start := time.Now()
	var res MirrorResult

	// 1) 先把远端树扫出来（目录层数受 MaxDepth 限制）
	var files []Entry
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		entries, err := src.List(ctx, dir)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
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
			files = append(files, e)
		}
		return nil
	}
	if err := walk("", 0); err != nil {
		return res, err
	}
	res.Scanned = len(files)
	opt.Logger.Printf("远端扫描完成：%d 个文件（并发 %d，最大层级 %d）", len(files), opt.Workers, opt.MaxDepth)

	// 2) 并发下载差异文件
	jobs := make(chan Entry, opt.Workers*4)
	var wg sync.WaitGroup
	var added, updated, failed, bytes int64
	var mu sync.Mutex
	seen := make(map[string]bool, len(files))

	for i := 0; i < opt.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range jobs {
				dst := filepath.Join(opt.Dest, filepath.FromSlash(e.Path))
				reason, need := needSync(dst, e)
				if !need {
					continue
				}
				if opt.DryRun {
					opt.Logger.Printf("[dry-run] %s %s", reason, e.Path)
					continue
				}
				n, err := downloadTo(ctx, src, e, dst)
				if err != nil {
					atomic.AddInt64(&failed, 1)
					opt.Logger.Printf("下载失败 %s: %v", e.Path, err)
					continue
				}
				atomic.AddInt64(&bytes, n)
				if reason == "新增" {
					atomic.AddInt64(&added, 1)
				} else {
					atomic.AddInt64(&updated, 1)
				}
			}
		}()
	}
	for _, e := range files {
		mu.Lock()
		seen[filepath.FromSlash(e.Path)] = true
		mu.Unlock()
		jobs <- e
	}
	close(jobs)
	wg.Wait()

	res.Added = int(added)
	res.Updated = int(updated)
	res.Failed = int(failed)
	res.Bytes = bytes

	// 3) 可选：清理本地多余文件
	if opt.Prune && !opt.DryRun {
		deleted := 0
		_ = filepath.WalkDir(opt.Dest, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, rerr := filepath.Rel(opt.Dest, p)
			if rerr != nil {
				return nil
			}
			if !seen[rel] {
				if strings.HasPrefix(filepath.Base(rel), ".") {
					return nil
				}
				if rerr := os.Remove(p); rerr == nil {
					deleted++
				}
			}
			return nil
		})
		res.Deleted = deleted
	}

	res.Duration = time.Since(start)
	opt.Logger.Printf("同步完成：扫描 %d，新增 %d，更新 %d，删除 %d，失败 %d，下载 %.1f MB，耗时 %s",
		res.Scanned, res.Added, res.Updated, res.Deleted, res.Failed,
		float64(res.Bytes)/1048576, res.Duration.Round(time.Millisecond))
	return res, nil
}

// needSync 判断是否需要下载，并给出原因。
func needSync(dst string, e Entry) (string, bool) {
	fi, err := os.Stat(dst)
	if err != nil {
		return "新增", true
	}
	if fi.IsDir() {
		return "新增", true
	}
	if fi.Size() != e.Size {
		return "更新(大小变化)", true
	}
	if !e.ModTime.IsZero() && e.ModTime.Sub(fi.ModTime()) > time.Second {
		return "更新(时间更新)", true
	}
	return "", false
}

// downloadTo 下载到临时文件再原子改名，并保留远端 mtime（服务端增量扫描依赖它）。
func downloadTo(ctx context.Context, src Source, e Entry, dst string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	rc, err := src.Open(ctx, e.Path)
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".mirror-*")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	n, copyErr := io.Copy(tmp, rc)
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(tmpName)
		if copyErr != nil {
			return 0, copyErr
		}
		return 0, closeErr
	}
	if e.Size > 0 && n != e.Size {
		_ = os.Remove(tmpName)
		return 0, fmt.Errorf("大小不符：期望 %d，实际 %d", e.Size, n)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return 0, err
	}
	if !e.ModTime.IsZero() {
		_ = os.Chtimes(dst, e.ModTime, e.ModTime)
	}
	return n, nil
}

func wantExt(p string, exts []string) bool {
	if len(exts) == 0 {
		return true
	}
	low := strings.ToLower(filepath.Ext(p))
	for _, e := range exts {
		if low == strings.ToLower(e) {
			return true
		}
	}
	return false
}
