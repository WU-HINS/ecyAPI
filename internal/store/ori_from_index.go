package store

// ori_from_index.go —— compact 模式下，方向信息以索引为准，不再维护 per-file 的 oriMeta 映射。
//
// 实测：5 万条时 oriMeta 占 6.3 MB（126 B/条），100 万条就是 126 MB——
// 这是「极低内存」目标里最大的一块可省项。

import "ecyapi/internal/index"

// oriFromIndex 从「方向索引」取某文件的方向。
//
// 方向索引是独立于读路径的一份 mmap（不参与代次失效），但用 **size + mtime 校验**：
// 只有和当前 stat 完全一致时才信任它的方向，否则回退读文件头。
// 这样既能省掉 per-file 的 oriMeta 映射，又不会用旧方向误导。
func (f *fileStore) oriFromIndex(cat, name string, size, modNano int64) (Orientation, int, int, bool) {
	f.oriMu.Lock()
	ix := f.oriIdx
	cats := f.oriCats
	f.oriMu.Unlock()
	if ix == nil || cats == nil {
		return OrientationUnknown, 0, 0, false
	}
	cc, ok := cats.get(cat)
	if !ok {
		return OrientationUnknown, 0, 0, false
	}
	rec, ok := ix.Lookup(cc.cat, name)
	if !ok {
		return OrientationUnknown, 0, 0, false
	}
	if int64(rec.Size) != size || int64(rec.MTime) != modNano/1e9 {
		return OrientationUnknown, 0, 0, false // 文件变了，方向不可信
	}
	switch rec.Flags & 0x3 {
	case index.OriLand:
		return OrientationLandscape, rec.W, rec.H, true
	case index.OriPort:
		return OrientationPortrait, rec.W, rec.H, true
	case index.OriSquare:
		// 方图按横图处理（与旧实现一致）
		return OrientationLandscape, rec.W, rec.H, true
	}
	return OrientationUnknown, rec.W, rec.H, true
}

// dropOriMeta compact 模式下释放 per-file 方向映射（方向已在索引里）。
func (f *fileStore) dropOriMeta() {
	f.mu.Lock()
	f.oriMeta = nil
	f.mu.Unlock()
}
