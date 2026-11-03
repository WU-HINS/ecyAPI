package store

// compact_index.go —— M2b-2：紧凑索引的落盘与启动校验。
//
// 与快照同一节奏保存（同一次扫描结果、同一个目录），但格式换成 internal/index：
// 定长记录 + mmap，100 万条也只有 41MB 落盘、打开后几乎不占堆。
//
// 本轮（M2b-2）只做「写出 + 启动校验」，查询仍走内存结构；
// 下一轮（M2b-3）再把读路径切过去。

import (
	"log"
	"os"
	"path/filepath"

	"ecyapi/internal/index"
)

// CompactIndexPath 紧凑索引的落盘位置（与数据目录同级，和快照放一起）。
func CompactIndexPath(dataDir string) string {
	return filepath.Join(filepath.Dir(dataDir), "index.bin")
}

// WriteCompactIndex 把当前内存索引导出成紧凑索引文件。
func (f *fileStore) WriteCompactIndex() (string, int, error) {
	path := CompactIndexPath(f.dir)
	n, err := f.BuildCompactIndex(path)
	return path, n, err
}

// VerifyCompactIndex 启动时校验紧凑索引与内存索引是否一致。
//
// 一致 → 返回 (true, 条目数)；不存在/读不了/对不上 → (false, 0)，调用方可以重建。
// 只做一次 O(1) 的条目数比对 + 分类数比对，不遍历内容（内容一致性由 M2a 的测试保证）。
func (f *fileStore) VerifyCompactIndex() (bool, int) {
	path := CompactIndexPath(f.dir)
	ix, err := index.Open(path)
	if err != nil {
		return false, 0
	}
	defer ix.Close()

	want := 0
	f.mu.RLock()
	for _, set := range f.cache {
		want += len(set.pick(OrientationAny))
	}
	f.mu.RUnlock()

	if ix.RecCount() != want {
		log.Printf("紧凑索引与内存不一致（文件 %d 条 / 内存 %d 条），将重建", ix.RecCount(), want)
		return false, ix.RecCount()
	}
	if len(ix.Categories()) != f.loadedCategories() {
		log.Printf("紧凑索引分类数不一致（文件 %d / 内存 %d），将重建",
			len(ix.Categories()), f.loadedCategories())
		return false, ix.RecCount()
	}
	return true, ix.RecCount()
}

// reopenOriIndex 重新打开方向索引（独立 mmap，供扫描时免读文件头判方向）。
func (f *fileStore) reopenOriIndex() {
	ix, err := index.Open(CompactIndexPath(f.dir))
	if err != nil {
		return
	}
	f.oriMu.Lock()
	if f.oriIdx != nil {
		_ = f.oriIdx.Close()
	}
	f.oriIdx = ix
	f.oriCats = newCompactCache(ix, 32)
	f.oriMu.Unlock()
}

// refreshCompactIndex 在快照之后顺带刷新紧凑索引（失败只记日志，不影响服务）。
func (f *fileStore) refreshCompactIndex() {
	path, n, err := f.WriteCompactIndex()
	if err != nil {
		log.Printf("写紧凑索引失败（不影响服务）: %v", err)
		return
	}
	if fi, statErr := os.Stat(path); statErr == nil {
		log.Printf("紧凑索引已更新：%d 条，%.1f MB（%s）", n, float64(fi.Size())/1048576, filepath.Base(path))
	}
	// 重新打开索引并释放 per-file 的 oriMeta（方向已在索引里）
	f.reopenOriIndex()
	if f.compactReadEnabled {
		f.openCompact()
		f.dropOriMeta()
		f.dropEntrySets()
	}
}

// dropEntrySets compact 模式下释放内存里的条目数组。
//
// 前提：聚合不再引用这些切片（compact 模式的父目录聚合直接从索引现算），
// 所以这里可以安全地把 cache 与 dirs[].set 一起清空；分类数与计数改由索引提供。
func (f *fileStore) dropEntrySets() {
	f.mu.Lock()
	f.cache = map[string]*categorySet{}
	f.aggs = map[string]*aggKids{}
	f.groupAggCache = nil
	for _, d := range f.dirs {
		d.set = nil
	}
	f.snapMu.Lock()
	f.sortedCats, f.counts = nil, nil
	f.snapMu.Unlock()
	f.mu.Unlock()
}
