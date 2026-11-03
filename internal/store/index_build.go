package store

// index_build.go —— 里程碑 2a：把当前内存索引导出成紧凑索引文件（internal/index），
// 并验证「同一份数据、两种表示」的结果一致。
//
// 这一步不改变服务行为：内存里还是老结构，但紧凑索引已经能完整表达同样的信息，
// 下一步（2b）才把查询路径切过去。

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"ecyapi/internal/index"
)

// BuildCompactIndex 把当前索引写成紧凑文件，返回条目数。
func (f *fileStore) BuildCompactIndex(path string) (int, error) {
	f.mu.RLock()
	cats := make([]string, 0, len(f.cache))
	for name := range f.cache {
		cats = append(cats, name)
	}
	sets := f.cache
	aggs := f.aggs
	f.mu.RUnlock()

	builder := &index.Builder{}
	total := 0
	for _, name := range cats {
		set, ok := sets[name]
		if !ok {
			continue
		}
		entries := set.pick(OrientationAny)
		if len(entries) == 0 {
			continue
		}
		cb := index.CatBuild{Name: name}
		if _, isAgg := aggs[name]; isAgg {
			cb.Flags |= index.FlagRemote // 仅占位：聚合目录不是远端，这里留给后续云端源区分
			cb.Flags &^= index.FlagRemote
		}
		for _, e := range entries {
			cb.Items = append(cb.Items, index.Entry{
				Name:  e.name,
				Size:  0, // 现有 fileEntry 不存 size/mtime；2b 切换时补齐
				MTime: 0,
				Flags: oriFlag(e.ori),
			})
		}
		builder.AddCategory(cb)
		total += len(cb.Items)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	if err := builder.Build(path); err != nil {
		return 0, fmt.Errorf("写紧凑索引失败: %w", err)
	}
	return total, nil
}

func oriFlag(o Orientation) uint8 {
	switch o {
	case OrientationLandscape:
		return index.OriLand
	case OrientationPortrait:
		return index.OriPort
	case OrientationAny:
		return index.OriUnknown
	default:
		return index.OriUnknown
	}
}

// CompactIndexPath 紧凑索引的默认落盘位置。
func CompactIndexPath(dataDir string) string {
	return filepath.Join(dataDir, "index.bin")
}

var _ = time.Now
