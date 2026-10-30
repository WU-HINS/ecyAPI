package server

// static.go — 内置静态资源（看板娘 /@index/*）。
//
// 仓库里只放「小文件」（脚本、配置、光标、字体，约 4 MB）；看板娘的动画 webm 有 50 MB，
// 不入库：由 tools/fetchassets 下载到运行目录（默认 data/static），
// 服务端按「磁盘优先、内置兜底」的顺序提供，所以缺动画时页面依然能用。

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

//go:embed all:static/index
var staticIndexFS embed.FS

// staticContentType 明确指定类型，避免不同平台的 mime 表差异。
func staticContentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".jsonc", ".json":
		return "application/json; charset=utf-8"
	case ".webm":
		return "video/webm"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".ttf":
		return "font/ttf"
	case ".txt":
		return "text/plain; charset=utf-8"
	}
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// syncStaticAssets 启动时把 staticSyncFrom 里的素材补进 staticDir。
//
// Docker 镜像把素材放在镜像层（/app/static），而运行目录 /app/data 通常是数据卷：
// 挂载会盖住镜像里的同名目录，所以启动时把「镜像里有、卷里没有」的文件补进去。
// 只对缺失或更新的文件做拷贝，已存在的只 stat 一次；卷只读时失败也不影响启动
// （读路径会回落到 staticSyncFrom）。
func (s *Server) syncStaticAssets() {
	src, dst := s.staticSyncFrom, s.staticDir
	if src == "" || dst == "" {
		return
	}
	if absSrc, err1 := filepath.Abs(src); err1 == nil {
		if absDst, err2 := filepath.Abs(dst); err2 == nil && absSrc == absDst {
			return
		}
	}
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return
	}
	start := time.Now()
	copied, updated, err := syncTree(src, dst)
	if err != nil {
		log.Printf("提示：静态资源同步失败（%v）；/@index 会直接读 %s，功能不受影响", err, src)
		return
	}
	if copied+updated > 0 {
		log.Printf("静态资源已就绪：新增 %d、更新 %d 个文件 → %s（耗时 %s）",
			copied, updated, dst, time.Since(start).Round(time.Millisecond))
	}
}

// syncTree 把 src 下的普通文件按「缺失或更新」复制到 dst。
// 返回新增/更新数量；目标目录不可写时返回错误。
func syncTree(src, dst string) (added, updated int, err error) {
	err = filepath.WalkDir(src, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil || rel == "." {
			return nil
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		sfi, serr := d.Info()
		if serr != nil {
			return nil
		}
		target := filepath.Join(dst, rel)
		dfi, derr := os.Stat(target)
		if derr == nil && dfi.Size() == sfi.Size() && !sfi.ModTime().After(dfi.ModTime()) {
			return nil // 已是最新
		}
		if err := copyFile(p, target, sfi); err != nil {
			return err
		}
		if derr == nil {
			updated++
		} else {
			added++
		}
		return nil
	})
	return added, updated, err
}

// copyFile 复制单个文件并保留 mtime（下次启动就能靠 mtime 判断是否需要更新）。
func copyFile(src, dst string, fi os.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Chtimes(dst, fi.ModTime(), fi.ModTime())
	return nil
}

// warnMissingPetAssets 启动时检查看板娘动画是否已安装（webm 体积大，不随二进制分发）。
// 缺动画时页面仍能用，但控制台会一直刷 404，所以在日志里明确提示一次。
func (s *Server) warnMissingPetAssets() {
	dir := filepath.Join(s.staticDir, "assets", "webm")
	entries, err := os.ReadDir(dir)
	if err == nil {
		n := 0
		for _, e := range entries {
			if strings.HasSuffix(strings.ToLower(e.Name()), ".webm") {
				n++
			}
		}
		if n > 0 {
			return
		}
	}
	log.Printf("提示：看板娘动画未安装（%s 下没有 .webm），首页会报动画 404；"+
		"执行 make assets、或改用带素材的完整包；Docker 下若把卷挂在 /app/data，"+
		"镜像里的素材会被卷遮住——新版镜像已改放到 /app/static，旧镜像可用 docker cp 取出素材", dir)
}

// staticVersionRe 匹配版本前缀（v + 6~16 位十六进制）。
var staticVersionRe = regexp.MustCompile(`^v[0-9a-f]{6,16}$`)

// staticCacheControl 静态资源的缓存策略：
//   - 带版本前缀（/@index/v<hash>/...）：immutable，可以吃满 max-age；
//   - 不带前缀：同样 max-age，但允许 revalidate（ServeContent 会回 304）。
func (s *Server) staticCacheControl(immutable bool) string {
	maxAge := s.cfg.StaticMaxAge
	if maxAge <= 0 {
		maxAge = 7 * 24 * 3600
	}
	if immutable {
		return fmt.Sprintf("public, max-age=%d, immutable", maxAge)
	}
	return fmt.Sprintf("public, max-age=%d", maxAge)
}

// staticRoots 返回磁盘资源的查找顺序（去重、跳过空值）。
func (s *Server) staticRoots() []string {
	out := make([]string, 0, 2)
	if s.staticDir != "" {
		out = append(out, s.staticDir)
	}
	if s.staticSyncFrom != "" && s.staticSyncFrom != s.staticDir {
		out = append(out, s.staticSyncFrom)
	}
	return out
}

// staticAssetVersion 计算静态资源版本号（前 8 位十六进制）。
// 用于 /@index/v<版本>/... 这种带版本前缀的 URL：前缀一变浏览器/CDN 自然回源，
// 所以可以放心给很长的缓存时间；不带前缀的 URL 仍然可用，但只给较短的缓存。
func (s *Server) staticAssetVersion() string {
	h := sha256.New()
	// 内置资源（脚本/配置/光标/字体）
	if sub, err := fs.Sub(staticIndexFS, "static/index"); err == nil {
		_ = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if fi, ferr := d.Info(); ferr == nil {
				fmt.Fprintf(h, "%s:%d;", p, fi.Size())
			}
			if data, rerr := fs.ReadFile(sub, p); rerr == nil {
				h.Write(data)
			}
			return nil
		})
	}
	// 磁盘素材（webm 等）：按名字+大小+修改时间摘要，不读内容（50MB 太贵）
	for _, root := range s.staticRoots() {
		_ = filepath.WalkDir(filepath.Join(root, "assets", "webm"), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if fi, ferr := d.Info(); ferr == nil {
				fmt.Fprintf(h, "%s:%d:%d;", p, fi.Size(), fi.ModTime().UnixNano())
			}
			return nil
		})
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:4]) // 8 位十六进制
}

// handleIndexStatic 服务 /@index/*：磁盘目录优先，内置 embed 兜底。
func (s *Server) handleIndexStatic(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/@index/")
	// 归一化并挡掉路径穿越：Clean 后必须仍在根内。
	clean := strings.TrimPrefix(path.Clean("/"+rel), "/")
	if clean == "" || clean == "." || strings.HasPrefix(clean, "..") {
		http.NotFound(w, r)
		return
	}
	// 版本前缀：/@index/v1a2b3c4d/xxx → 去掉前缀，并按「不可变」下发缓存头。
	immutable := false
	if first, rest, ok := strings.Cut(clean, "/"); ok && staticVersionRe.MatchString(first) && rest != "" {
		clean = rest
		immutable = true
	}

	// 磁盘目录按顺序找：运行目录（数据卷）→ 镜像层目录。卷只读或还没同步时也能用。
	for _, root := range s.staticRoots() {
		p := filepath.Join(root, filepath.FromSlash(clean))
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		fi, statErr := f.Stat()
		if statErr != nil || !fi.Mode().IsRegular() {
			_ = f.Close()
			continue
		}
		w.Header().Set("Content-Type", staticContentType(clean))
		w.Header().Set("Cache-Control", s.staticCacheControl(immutable))
		http.ServeContent(w, r, clean, fi.ModTime(), f)
		_ = f.Close()
		return
	}

	sub, err := fs.Sub(staticIndexFS, "static/index")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := sub.Open(clean)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", staticContentType(clean))
	w.Header().Set("Cache-Control", s.staticCacheControl(immutable))
	// embed 文件没有有意义的 mtime，用零值让 ServeContent 只做 Range 处理。
	http.ServeContent(w, r, clean, time.Time{}, rs)
}
