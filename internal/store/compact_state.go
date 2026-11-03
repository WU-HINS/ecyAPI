package store

// compact_state.go —— 紧凑索引的打开/刷新与「代次一致」保护。
//
// 读路径只在该索引的**代次与当前索引一致**时才启用；扫描后代次 +1，
// 在重新打开之前自动退回内存路径，避免读到旧数据。

import (
	"log"
	"sort"
	"sync"

	"ecyapi/internal/index"
)

type compactState struct {
	ix   *index.Index
	cats *compactCache
	gen  uint64
	// catsSnapshot 分类名+计数的缓存（与索引同代次），避免每个请求重新遍历 1000 个分类。
	snapOnce sync.Once
	names    []string
	counts   map[string]int
}

// openCompact 打开紧凑索引（失败只记日志，读路径继续走内存）。
func (f *fileStore) openCompact() {
	ix, err := index.Open(CompactIndexPath(f.dir))
	if err != nil {
		if f.compact != nil {
			log.Printf("紧凑索引不可用，读路径回退内存: %v", err)
		}
		f.compact = nil
		return
	}
	gen := f.gen.Load()
	if f.compact != nil && f.compact.ix != nil {
		_ = f.compact.ix.Close()
	}
	f.compact = &compactState{ix: ix, cats: newCompactCache(ix, 64), gen: gen}
}

// compactReady 读路径是否可用（开关打开 + 已打开 + 代次一致）。
func (f *fileStore) compactReady() *compactState {
	c := f.compact
	if c == nil || c.ix == nil {
		return nil
	}
	if c.gen != f.gen.Load() {
		return nil // 索引已过期，等下一次 openCompact
	}
	return c
}

// closeCompact 释放 mmap。
func (f *fileStore) closeCompact() {
	if f.compact != nil && f.compact.ix != nil {
		_ = f.compact.ix.Close()
		f.compact = nil
	}
}

// snapshot 返回（并缓存）分类名与计数。
func (c *compactState) snapshot() ([]string, map[string]int) {
	c.snapOnce.Do(func() {
		all := c.ix.Categories()
		c.names = make([]string, 0, len(all))
		c.counts = make(map[string]int, len(all))
		for _, cat := range all {
			c.names = append(c.names, cat.Name)
			c.counts[cat.Name] = cat.Count
		}
		sort.Strings(c.names)
	})
	return c.names, c.counts
}
