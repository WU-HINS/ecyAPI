package server

import (
	"net/http"
	"os"
	"strconv"
	"strings"
)

// contentTypeByExt 对外输出白名单：只有这些扩展名会被 /pic 输出，
// 分类目录里的其他文件（.txt/.sql/备份等）一律 404。
var contentTypeByExt = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".avif": "image/avif",
	".svg":  "image/svg+xml",
	".mp4":  "video/mp4",
	".webm": "video/webm",
}

// contentTypeFor 按扩展名取 Content-Type；不在白名单返回 false。
func contentTypeFor(name string) (string, bool) {
	ct, ok := contentTypeByExt[strings.ToLower(extOf(name))]
	return ct, ok
}

func extOf(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
}

// etagFor 生成弱 ETag：文件内容随 (mtime, size) 变化，两者相同即视为同一版本。
func etagFor(fi os.FileInfo) string {
	return `"` + strconv.FormatInt(fi.ModTime().UnixNano(), 16) + "-" +
		strconv.FormatInt(fi.Size(), 16) + `"`
}

// serveLocalFile 输出本地图片/视频。
//
// 在标准库 ServeContent 之上补三件事：ETag（内容随 mtime+size 变化）、
// X-Content-Type-Options: nosniff、以及 SVG 的 CSP 沙箱。
// 传输本身仍走 ServeContent：它内部用 SectionReader.WriteTo，
// 在真实 http.response 上落到 sendfile，比自建 io.Copy 分配更少。
func serveLocalFile(w http.ResponseWriter, r *http.Request, f *os.File, fi os.FileInfo, ct string) {
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	// SVG 可内联脚本：沙箱化，禁止脚本与同源能力，防止被当成 XSS 载体。
	if ct == "image/svg+xml" {
		h.Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'")
	}
	etag := etagFor(fi)
	h.Set("ETag", etag)
	if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatch(inm, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// etagMatch 判断 If-None-Match 是否命中（支持逗号分隔与 *）。
func etagMatch(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || part == etag || strings.TrimPrefix(part, "W/") == etag {
			return true
		}
	}
	return false
}

// cacheLong 图片与缩略图的强缓存头（7 天 + SWR）。
func cacheLong(h http.Header) {
	h.Set("Cache-Control", "public, max-age=604800, stale-while-revalidate=86400")
}
