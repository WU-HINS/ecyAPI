package store

// roots.go —— 多图库路径：主库 + 若干附加库。
//
// 设计（见 docs/multi-root-plan.md）：
//   - 主库：分类名不带前缀，URL 与行为完全不变
//   - 附加库：分类名带不可见命名空间前缀 "\x01<库名>\x01"
//   - URL 层由 server 负责把 /lib/<库名>/ 映射到该前缀

import (
	"log"
	"path/filepath"
	"strings"
)

// nsSep 命名空间分隔符（不可见，避免与真实目录名冲突）。
const nsSep = "\x01"

// Root 一个图库根目录。
type Root struct {
	Name string // 主库为空串；附加库如 "网盘"
	Path string // 真实目录
	Main bool
}

// namespacePrefix 附加库的分类名前缀；主库返回空串。
func (r Root) namespacePrefix() string {
	if r.Main || r.Name == "" {
		return ""
	}
	return nsSep + r.Name
}

// matches 判断 rel 是否属于该根。
func (r Root) matches(rel string) bool {
	p := r.namespacePrefix()
	if p == "" {
		return false
	}
	return rel == p || strings.HasPrefix(rel, p+"/")
}

// Roots 当前全部图库根目录（主库在首位）。
func (f *fileStore) Roots() []Root {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]Root, len(f.roots))
	copy(out, f.roots)
	return out
}

// rootOf 返回分类所属的根；找不到返回主库。
func (f *fileStore) rootOf(rel string) Root {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, r := range f.roots {
		if r.matches(rel) {
			return r
		}
	}
	for _, r := range f.roots {
		if r.Main {
			return r
		}
	}
	if len(f.roots) > 0 {
		return f.roots[0]
	}
	return Root{Path: f.dir, Main: true}
}

// physPath 把「分类路径（可带命名空间前缀）」映射到真实文件系统路径。
// rel 为空表示主库根。
func (f *fileStore) physPath(rel string) string {
	r := f.rootOf(rel)
	clean := rel
	if p := r.namespacePrefix(); p != "" && r.matches(rel) {
		clean = strings.TrimPrefix(rel, p)
		clean = strings.TrimPrefix(clean, "/")
	}
	if clean == "" {
		return r.Path
	}
	return filepath.Join(r.Path, filepath.FromSlash(clean))
}

// StripNamespace 去掉分类名的命名空间前缀，返回 (库名, 库内相对路径)。
// 主库返回 ("", rel)。
func StripNamespace(rel string) (string, string) {
	if !strings.HasPrefix(rel, nsSep) {
		return "", rel
	}
	rest := rel[len(nsSep):]
	i := strings.Index(rest, "/")
	if i < 0 {
		return rest, ""
	}
	return rest[:i], rest[i+1:]
}

// JoinNamespace 把库名与库内相对路径拼成带命名空间的分类名。
// 库名为空（主库）时原样返回。
func JoinNamespace(lib, rel string) string {
	if lib == "" {
		return rel
	}
	if rel == "" {
		return nsSep + lib
	}
	return nsSep + lib + "/" + rel
}

// AbsPath 解析分类下某个文件的真实路径（供 /pic 等服务端路径使用）。
func (f *fileStore) AbsPath(category, file string) (string, bool) {
	if category == "" || file == "" || strings.ContainsAny(file, "/\\") {
		return "", false
	}
	base := f.physPath(category)
	if base == "" {
		return "", false
	}
	return filepath.Join(base, file), true
}

// LibraryOf 返回分类所属的库名（主库返回 ""）。
func LibraryOf(rel string) string {
	lib, _ := StripNamespace(rel)
	return lib
}

// SetExtraRoots 替换附加图库列表（保留主库），并触发一次重扫。
// 供管理接口增删附加库使用；返回错误仅表示扫描启动失败。
func (f *fileStore) SetExtraRoots(roots []Root) {
	f.mu.Lock()
	main := f.roots[:1]
	if len(main) == 0 {
		main = []Root{{Path: f.dir, Main: true}}
	}
	f.roots = append(append([]Root{}, main...), roots...)
	f.mu.Unlock()
	// 扫描会重建 dirs/cache：已删除库的分类自然消失
	go func() {
		if err := f.scan(); err != nil {
			log.Printf("附加图库变更后重扫失败: %v", err)
			return
		}
		f.gen.Add(1)
		f.dirty.Store(true)
	}()
}
