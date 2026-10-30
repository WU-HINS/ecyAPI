package server

// static.go — 内置静态资源（看板娘 /@index/*）。
//
// 仓库里只放「小文件」（脚本、配置、光标、字体，约 4 MB）；看板娘的动画 webm 有 50 MB，
// 不入库：由 tools/fetchassets 下载到运行目录（默认 data/static），
// 服务端按「磁盘优先、内置兜底」的顺序提供，所以缺动画时页面依然能用。

import (
	"embed"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
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

// handleIndexStatic 服务 /@index/*：磁盘目录优先，内置 embed 兜底。
func (s *Server) handleIndexStatic(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/@index/")
	// 归一化并挡掉路径穿越：Clean 后必须仍在根内。
	clean := strings.TrimPrefix(path.Clean("/"+rel), "/")
	if clean == "" || clean == "." || strings.HasPrefix(clean, "..") {
		http.NotFound(w, r)
		return
	}

	if s.staticDir != "" {
		p := filepath.Join(s.staticDir, filepath.FromSlash(clean))
		if f, err := os.Open(p); err == nil {
			if fi, err := f.Stat(); err == nil && fi.Mode().IsRegular() {
				defer f.Close()
				w.Header().Set("Content-Type", staticContentType(clean))
				w.Header().Set("Cache-Control", "public, max-age=86400")
				http.ServeContent(w, r, clean, fi.ModTime(), f)
				return
			}
			_ = f.Close()
		}
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
	w.Header().Set("Cache-Control", "public, max-age=86400")
	// embed 文件没有有意义的 mtime，用零值让 ServeContent 只做 Range 处理。
	http.ServeContent(w, r, clean, time.Time{}, rs)
}
