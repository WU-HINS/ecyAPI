// Package petassets 下载看板娘（dsh-pet）的静态资源。
//
// 仓库只内置脚本/配置/光标/字体；动画 webm 约 50 MB，不随二进制分发。
// 两种用法：
//   - 命令行：go run ./tools/fetchassets -dir data/static
//   - 服务端内置：ecyapi -fetch-assets（Docker 里补素材最方便）
package petassets

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

// DefaultBase 上游素材地址。
const DefaultBase = "https://pet.alcy.cc"

// Result 下载结果统计。
type Result struct {
	Added   int
	Skipped int
	Failed  int
}

// Fetch 把素材下载到 dir（会创建子目录），已存在的文件跳过。
func Fetch(dir, base string, logf func(format string, args ...any)) (Result, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	f := &fetcher{
		client: &http.Client{Timeout: 60 * time.Second},
		base:   strings.TrimRight(base, "/"),
		dir:    dir,
	}
	var res Result

	// 1) 脚本、配置、光标、字体
	must := []struct{ urlPath, rel string }{
		{"/dsh-pet.standalone.js", "dsh-pet.standalone.js"},
		{"/assets/config.jsonc", "assets/config.jsonc"},
	}
	for _, m := range must {
		if err := f.get(m.urlPath, m.rel); err != nil {
			return res, fmt.Errorf("下载 %s 失败: %w", m.rel, err)
		}
	}
	for _, p := range []string{"cursor-grab.png", "cursor-grabbing.png"} {
		if err := f.get("/assets/pic/"+p, "assets/pic/"+p); err != nil {
			logf("下载光标失败 %s: %v", p, err)
		}
	}
	font := "上首软糖体.ttf"
	if err := f.get("/assets/fonts/"+url.PathEscape(font), "assets/fonts/"+font); err != nil {
		logf("下载字体失败: %v", err)
	}

	// 2) 动画
	names, err := f.animationNames()
	if err != nil {
		return res, fmt.Errorf("解析配置失败: %w", err)
	}
	logf("配置里共 %d 个候选动画名", len(names))
	var mu sync.Mutex
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, name := range names {
		rel := "assets/webm/" + name + ".webm"
		if fileExists(filepath.Join(dir, filepath.FromSlash(rel))) {
			mu.Lock()
			res.Skipped++
			mu.Unlock()
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(name, rel string) {
			defer wg.Done()
			defer func() { <-sem }()
			err := f.get("/assets/webm/"+url.PathEscape(name)+".webm", rel)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				res.Failed++ // 配置里的分组标题也会被当成候选，缺文件属正常
				return
			}
			res.Added++
		}(name, rel)
	}
	wg.Wait()
	logf("完成：新增 %d，已存在 %d，缺失/跳过 %d，目录 %s", res.Added, res.Skipped, res.Failed, dir)
	return res, nil
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

func (f *fetcher) get(urlPath, rel string) error {
	dst := filepath.Join(f.dir, filepath.FromSlash(rel))
	if fileExists(dst) {
		return nil
	}
	req, err := http.NewRequest(http.MethodGet, f.base+urlPath, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ecyapi-assets/1.0")
	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	// CDN 出错时会返回 HTML 错误页，别把垃圾写进目录
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/html") && !strings.HasSuffix(rel, ".txt") {
		return fmt.Errorf("返回了 HTML（%d 字节）", resp.ContentLength)
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

var quoteRe = regexp.MustCompile(`"([^"\n]{1,40})"`)

func (f *fetcher) animationNames() ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(f.dir, "assets/config.jsonc"))
	if err != nil {
		return nil, err
	}
	text := stripJSONC(string(raw))
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
