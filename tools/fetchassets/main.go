// Command fetchassets 把看板娘的静态资源下载到本地目录。
//
// 仓库里只内置了「小文件」（脚本/配置/光标/字体）；动画 webm 约 50 MB，不随仓库分发。
// 用法：
//
//	go run ./tools/fetchassets                  # 下载到 data/static（默认）
//	go run ./tools/fetchassets -dir /srv/static # 指定目录
//
// 服务端按 ECY_STATIC_DIR（默认 data/static）读取，磁盘优先、内置兜底。
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

func main() {
	dir := flag.String("dir", "data/static", "下载目标目录")
	base := flag.String("base", "https://pet.alcy.cc", "资源根地址")
	flag.Parse()

	client := &http.Client{Timeout: 60 * time.Second}
	ctx := &fetcher{client: client, base: strings.TrimRight(*base, "/"), dir: *dir}

	// 1) 脚本本体与配置
	if err := ctx.get("/dsh-pet.standalone.js", "dsh-pet.standalone.js"); err != nil {
		log.Fatalf("下载脚本失败: %v", err)
	}
	if err := ctx.get("/assets/config.jsonc", "assets/config.jsonc"); err != nil {
		log.Fatalf("下载配置失败: %v", err)
	}
	for _, p := range []string{"cursor-grab.png", "cursor-grabbing.png"} {
		if err := ctx.get("/assets/pic/"+p, "assets/pic/"+p); err != nil {
			log.Printf("下载光标失败 %s: %v", p, err)
		}
	}
	font := "上首软糖体.ttf"
	if err := ctx.get("/assets/fonts/"+url.PathEscape(font), "assets/fonts/"+font); err != nil {
		log.Printf("下载字体失败: %v", err)
	}

	// 2) 从配置里抽出动画名，逐个下载 webm
	names, err := ctx.animationNames()
	if err != nil {
		log.Fatalf("解析配置失败: %v", err)
	}
	log.Printf("配置里共 %d 个候选动画名", len(names))
	ok, miss, skip := 0, 0, 0
	for _, name := range names {
		rel := "assets/webm/" + name + ".webm"
		if fileExists(filepath.Join(*dir, filepath.FromSlash(rel))) {
			skip++
			continue
		}
		if err := ctx.get("/assets/webm/"+url.PathEscape(name)+".webm", rel); err != nil {
			miss++ // 配置里的分组标题也会被当成候选，缺文件属正常
			continue
		}
		ok++
	}
	log.Printf("完成：新增 %d，已存在 %d，缺失/跳过 %d，目录 %s", ok, skip, miss, *dir)
}

type fetcher struct {
	client *http.Client
	base   string
	dir    string
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Size() > 0
}

// get 下载 urlPath 到本地 rel；已存在则跳过。
func (f *fetcher) get(urlPath, rel string) error {
	dst := filepath.Join(f.dir, filepath.FromSlash(rel))
	if fileExists(dst) {
		return nil
	}
	req, err := http.NewRequest(http.MethodGet, f.base+urlPath, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ecyapi-fetchassets/1.0")
	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	// CDN 出错时会返回 HTML 错误页，长度对不上就算了，别把垃圾写进目录
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/html") && !strings.HasSuffix(rel, ".txt") {
		return fmt.Errorf("返回了 HTML（%d）", resp.ContentLength)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	fh, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(fh, resp.Body); err != nil {
		_ = fh.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := fh.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// 只匹配单行内的字符串（排除换行），避免跨行把结构字符也吃进去。
var quoteRe = regexp.MustCompile(`"([^"\n]{1,40})"`)

// animationNames 把 config.jsonc 去掉注释后，抽出「看起来像动画名」的中文字符串。
// 配置里的分组标题也会被抽到，下载时 404 直接跳过即可。
func (f *fetcher) animationNames() ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(f.dir, "assets/config.jsonc"))
	if err != nil {
		return nil, err
	}
	text := stripJSONC(string(raw))
	// 动画池都在 "animations" 之后；之前是人设提示词，避免把整段话当动画名。
	if i := strings.Index(text, `"animations"`); i >= 0 {
		text = text[i:]
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range quoteRe.FindAllStringSubmatch(text, -1) {
		name := m[1]
		if seen[name] || !hasCJK(name) || len([]rune(name)) > 24 || strings.HasSuffix(name, "。") {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

func stripJSONC(s string) string {
	var b strings.Builder
	inBlock := false
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case inBlock:
			if i := strings.Index(t, "*/"); i >= 0 {
				inBlock = false
				t = t[i+2:]
			} else {
				continue
			}
		case strings.HasPrefix(t, "//"):
			continue
		case strings.HasPrefix(t, "/*"):
			if i := strings.Index(t, "*/"); i >= 0 {
				t = t[i+2:]
			} else {
				inBlock = true
				continue
			}
		}
		b.WriteString(t)
		b.WriteString("\n")
	}
	return b.String()
}

func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}
