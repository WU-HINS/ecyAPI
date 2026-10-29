package store

// snapshot.go — 把内存索引落到磁盘，重启时直接读回，跳过全量扫描。
//
// 设计：
//   - 只存「扫描出来的事实」：每个目录的条目名、子目录、以及该目录作为分类时的文件列表；
//   - 聚合视图（父目录）在恢复时用内存里的集合重建，不占磁盘；
//   - 快照有 TTL（ECY_SNAPSHOT_TTL，默认 15 分钟）：太旧就不用，老老实实全量扫；
//   - 恢复后 dirState.verified 置为当前时间，随后每 3 秒的后台刷新先比条目名，
//     名字变了才重建那个目录 —— 停机期间增删的图片几秒内就会被纠正。

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const snapshotVersion = 1

type snapFile struct {
	Name string `json:"n"`
	Link string `json:"l"`
	W    int    `json:"w"`
	H    int    `json:"h"`
	Ori  int    `json:"o"`
}

type snapDir struct {
	Names   []string   `json:"names"`
	Subdirs []string   `json:"subdirs"`
	Skipped []string   `json:"skipped,omitempty"`
	Files   []snapFile `json:"files,omitempty"`
}

type snapDoc struct {
	Version int                 `json:"version"`
	SavedAt time.Time           `json:"saved_at"`
	Dirs    map[string]*snapDir `json:"dirs"`
}

// snapshotPath 快照放在数据源目录的旁边（data/index.snapshot.gz），不进分类树。
func (f *fileStore) snapshotPath() string {
	return filepath.Join(filepath.Dir(f.dir), "index.snapshot.gz")
}

// saveSnapshot 原子写快照（gzip + JSON）。
func (f *fileStore) saveSnapshot() error {
	f.reloadMu.Lock()
	defer f.reloadMu.Unlock()

	doc := snapDoc{Version: snapshotVersion, SavedAt: time.Now(), Dirs: make(map[string]*snapDir, len(f.dirs))}
	f.mu.RLock()
	for rel, d := range f.dirs {
		sd := &snapDir{Names: d.names, Subdirs: d.subdirs, Skipped: d.skipped}
		if set := f.cache[rel]; set != nil {
			files := make([]snapFile, len(set.all))
			for i, e := range set.all {
				files[i] = snapFile{Name: e.name, Link: e.link, W: e.w, H: e.h, Ori: int(e.ori)}
			}
			sd.Files = files
		}
		doc.Dirs[rel] = sd
	}
	f.mu.RUnlock()

	path := f.snapshotPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	fh, err := os.Create(tmp)
	if err != nil {
		return err
	}
	zw, _ := gzip.NewWriterLevel(fh, gzip.BestSpeed)
	if err := json.NewEncoder(zw).Encode(&doc); err != nil {
		_ = fh.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := zw.Close(); err != nil {
		_ = fh.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := fh.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	f.dirty.Store(false)
	return nil
}

// loadSnapshot 尝试用快照恢复；返回 false 表示没有可用快照（走全量扫描）。
func (f *fileStore) loadSnapshot(maxAge time.Duration) (bool, error) {
	fh, err := os.Open(f.snapshotPath())
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer fh.Close()
	zr, err := gzip.NewReader(fh)
	if err != nil {
		return false, fmt.Errorf("快照解压失败: %w", err)
	}
	defer zr.Close()
	var doc snapDoc
	if err := json.NewDecoder(zr).Decode(&doc); err != nil {
		return false, fmt.Errorf("快照解析失败: %w", err)
	}
	if doc.Version != snapshotVersion {
		return false, fmt.Errorf("快照版本不匹配（%d）", doc.Version)
	}
	if maxAge > 0 && time.Since(doc.SavedAt) > maxAge {
		return false, fmt.Errorf("快照已过期（%s 前）", time.Since(doc.SavedAt).Round(time.Minute))
	}

	now := time.Now().UnixNano()
	dirs := make(map[string]*dirState, len(doc.Dirs))
	cache := make(map[string]*categorySet, len(doc.Dirs))
	for rel, sd := range doc.Dirs {
		if sd == nil {
			continue
		}
		d := &dirState{names: sd.Names, subdirs: sd.Subdirs, skipped: sd.Skipped, verified: now}
		if len(sd.Files) > 0 {
			set := &categorySet{}
			for _, x := range sd.Files {
				set.add(fileEntry{name: x.Name, link: x.Link, w: x.W, h: x.H, ori: Orientation(x.Ori)})
			}
			set.sort()
			d.set = set
			cache[rel] = set
		}
		dirs[rel] = d
	}

	// 聚合视图按深度从深到浅重建（父目录依赖子目录的聚合）。
	rels := make([]string, 0, len(dirs))
	for rel := range dirs {
		rels = append(rels, rel)
	}
	sort.Slice(rels, func(i, j int) bool {
		return strings.Count(rels[i], "/") > strings.Count(rels[j], "/")
	})
	aggs := map[string]*aggKids{}
	sc := &scanCtx{sets: cache, aggs: aggs}
	for _, rel := range rels {
		d := dirs[rel]
		if d.set == nil {
			f.buildAgg(rel, d.subdirs, sc)
		}
	}

	f.mu.Lock()
	f.cache, f.aggs = cache, aggs
	f.mu.Unlock()
	f.dirs = dirs
	f.scannedOnce.Store(true)
	f.gen.Add(1) // 让图库等外层缓存键变化，避免用上重启前的旧缓存
	return true, nil
}

// snapshotLoop 按 TTL 周期把内存索引落盘（仅在有变化时写）。
func (f *fileStore) snapshotLoop(ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	t := time.NewTicker(ttl)
	defer t.Stop()
	for range t.C {
		// 每个周期都重写一次：即便索引没变化也刷新 SavedAt。
		// 否则「图库长期没人动」时快照会一直变旧，超过 TTL 后重启又要全量扫，
		// 白白浪费一次冷启动（重写 400KB/5万张，代价可忽略）。
		if err := f.saveSnapshot(); err != nil {
			// 只读挂载（多实例只读副本）会一直失败：只提示一次，别刷屏。
			if !f.snapshotErrLogged.Swap(true) {
				log.Printf("写快照失败（后续不再提示，只读部署可设 ECY_SNAPSHOT_TTL=0 关闭）: %v", err)
			}
		}
	}
}
