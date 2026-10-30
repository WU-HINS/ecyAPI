package server

// groups.go — 虚拟分组：把若干已存在的分类组合成一个「类似父目录」的分组，
// 挂载在 /@img/<name> 下（单级，不支持嵌套）。
//
// 定义放在 data/groups.json：
//   {"groups":[{"name":"wallpaper","label":"壁纸合集","desc":"桌面+移动",
//               "categories":["pc/hd","pc/4k"]}]}
//
// 页面 /@img/<name> 展示成员分类的全部图片（与 /img 同一套模板与缓存），
// 加 ?json / ?orientation= / ?w= 则返回该分组的一条随机链接。

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"ecyapi/internal/store"
)

const groupsFile = "data/groups.json"

// groupNameRe 单级名字：小写字母/数字开头，允许 - 和 _。
var groupNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// virtualGroup 一个虚拟分组。
type virtualGroup struct {
	Name       string   `json:"name"`
	Label      string   `json:"label"`
	Desc       string   `json:"desc,omitempty"`
	Categories []string `json:"categories"`
}

// groupsDoc 磁盘格式。
type groupsDoc struct {
	Groups []virtualGroup `json:"groups"`
}

// groupSnapshot 运行期的只读快照。
type groupSnapshot struct {
	byName map[string]virtualGroup
	list   []virtualGroup
	hash   string // 定义摘要，用于页面缓存键
}

// loadGroups 读取 data/groups.json；文件不存在视为没有分组。
func (s *Server) loadGroups() error {
	snap, err := readGroups(groupsFile)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.groups = snap
	s.mu.Unlock()
	return nil
}

// readGroups 解析并校验分组定义文件。
func readGroups(path string) (*groupSnapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &groupSnapshot{byName: map[string]virtualGroup{}}, nil
		}
		return nil, err
	}
	var doc groupsDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	snap := &groupSnapshot{byName: make(map[string]virtualGroup, len(doc.Groups))}
	h := sha1.New()
	for _, g := range doc.Groups {
		if !groupNameRe.MatchString(g.Name) {
			return nil, fmt.Errorf("分组名 %q 不合法：只能用小写字母/数字，允许 - 和 _，最长 32，且不能带 /", g.Name)
		}
		if strings.ContainsAny(g.Name, "/") {
			return nil, fmt.Errorf("分组名 %q 不能包含 /（只支持单级）", g.Name)
		}
		if _, dup := snap.byName[g.Name]; dup {
			return nil, fmt.Errorf("分组名 %q 重复", g.Name)
		}
		if len(g.Categories) == 0 {
			return nil, fmt.Errorf("分组 %q 至少要包含一个分类", g.Name)
		}
		seen := map[string]bool{}
		members := make([]string, 0, len(g.Categories))
		for _, c := range g.Categories {
			c = strings.Trim(strings.TrimSpace(c), "/")
			if c == "" || seen[c] {
				continue
			}
			seen[c] = true
			members = append(members, c)
		}
		if len(members) == 0 {
			return nil, fmt.Errorf("分组 %q 的分类列表为空", g.Name)
		}
		g.Categories = members
		if g.Label == "" {
			g.Label = g.Name
		}
		fmt.Fprintf(h, "%s|%s|%s;", g.Name, g.Label, strings.Join(members, ","))
		snap.byName[g.Name] = g
		snap.list = append(snap.list, g)
	}
	sort.Slice(snap.list, func(i, j int) bool { return snap.list[i].Name < snap.list[j].Name })
	snap.hash = hex.EncodeToString(h.Sum(nil)[:4])
	return snap, nil
}

// groupsSnapshot 返回当前分组快照。
func (s *Server) groupsSnapshot() *groupSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.groups == nil {
		return &groupSnapshot{byName: map[string]virtualGroup{}}
	}
	return s.groups
}

// groupByName 按名字取分组。
func (s *Server) groupByName(name string) (virtualGroup, bool) {
	g, ok := s.groupsSnapshot().byName[name]
	return g, ok
}

// handleGroup 处理 /@img/<name>：
//   - 默认渲染该分组的图库页（与 /img 同模板，带缓存）；
//   - ?json / ?json=N 返回随机链接（纯文本）；
//   - ?orientation=、?w= 与其他接口一致。
func (s *Server) handleGroup(w http.ResponseWriter, r *http.Request, name string) {
	g, ok := s.groupByName(name)
	if !ok {
		plainError(w, "No such group.", http.StatusNotFound)
		return
	}
	want := linkOrientation(r, endpoint{auto: true})
	// 与分类接口一致：该方向没有图时回退到「不限方向」，避免竖图分组在桌面端 404。
	groupWant := func() store.Orientation {
		if n, _ := s.st.GroupCount(r.Context(), g.Categories, want); n == 0 && want != store.OrientationAny {
			return store.OrientationAny
		}
		return want
	}
	want = groupWant()
	suffix := ""
	if v := r.URL.Query().Get("w"); v != "" {
		suffix = "?w=" + url.QueryEscape(v)
	}

	// ?json 分支：随机链接（与 /{分类}?json 行为一致）
	if _, hasJSON := r.URL.Query()["json"]; hasJSON {
		limit := positiveParam(r, "quantity", 1, s.cfg.MaxLinks)
		if v := r.URL.Query().Get("json"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		rows, err := s.st.GroupRandom(r.Context(), g.Categories, want, limit)
		if err != nil || len(rows) == 0 {
			plainError(w, "No links found.", http.StatusNotFound)
			return
		}
		if limit == 1 {
			plainText(w, s.absoluteURL(r, rows[0].Link)+suffix)
			return
		}
		links := make([]string, 0, len(rows))
		for _, row := range rows {
			links = append(links, s.absoluteURL(r, row.Link)+suffix)
		}
		plainText(w, strings.Join(links, "\n"))
		return
	}

	// 页面：缓存键 = 索引代次 + 分组定义摘要（定义变了自动换缓存）
	gen := s.st.Generation()
	dir := fmt.Sprintf("%d-%s", gen, s.groupsSnapshot().hash)
	cachePath := filepath.Join(s.galleryCacheRoot(), dir, "@img-"+g.Name+".html")
	if s.serveGalleryCache(w, r, cachePath) {
		return
	}

	total, err := s.st.GroupCount(r.Context(), g.Categories, want)
	if err != nil {
		total = 0
	}
	limit := s.cfg.GalleryMax
	if limit <= 0 || limit > total {
		limit = total
	}
	rows, err := s.st.GroupRandom(r.Context(), g.Categories, want, limit)
	if err != nil {
		rows = nil
	}
	images := make([]galleryImage, 0, len(rows))
	for _, row := range rows {
		images = append(images, galleryImage{URL: row.Link, Thumb: row.Link + "?w=480"})
	}
	type cat struct {
		Name  string
		Count int
	}
	catList := make([]cat, 0, len(g.Categories))
	for _, member := range g.Categories {
		n, _ := s.st.CountWith(r.Context(), member, want)
		catList = append(catList, cat{Name: member, Count: n})
	}
	data := map[string]any{
		"Current":    g.Label,
		"Images":     images,
		"Total":      total,
		"Shown":      len(images),
		"Sampled":    total > len(images),
		"Categories": catList,
		"LoadError":  false,
	}
	body, err := s.renderGalleryHTML(data)
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	s.writeGalleryCache(cachePath, body)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// handleAdminGroupsSave 保存分组定义（后台 JSON 编辑器）。
func (s *Server) handleAdminGroupsSave(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	raw := strings.TrimSpace(r.PostFormValue("groups_json"))
	if raw == "" {
		raw = `{"groups":[]}`
	}
	// 先落到临时文件校验，再原子替换
	tmp := groupsFile + ".tmp"
	if err := os.MkdirAll(filepath.Dir(groupsFile), 0o755); err != nil {
		s.adminResult(w, "保存失败: "+err.Error())
		return
	}
	if err := os.WriteFile(tmp, []byte(raw), 0o644); err != nil {
		s.adminResult(w, "保存失败: "+err.Error())
		return
	}
	snap, err := readGroups(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		s.adminResult(w, "定义有误: "+err.Error())
		return
	}
	if err := os.Rename(tmp, groupsFile); err != nil {
		_ = os.Remove(tmp)
		s.adminResult(w, "保存失败: "+err.Error())
		return
	}
	s.mu.Lock()
	s.groups = snap
	s.mu.Unlock()
	log.Printf("管理操作：保存虚拟分组 %d 个", len(snap.list))
	s.adminResult(w, fmt.Sprintf("已保存 %d 个虚拟分组：%s", len(snap.list), groupNames(snap)))
}

// groupsJSON 把当前分组渲染成可编辑的 JSON（后台文本框用）。
func groupsJSON(snap *groupSnapshot) string {
	doc := groupsDoc{Groups: make([]virtualGroup, 0, len(snap.list))}
	doc.Groups = append(doc.Groups, snap.list...)
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return `{"groups":[]}`
	}
	return string(b)
}

func groupNames(snap *groupSnapshot) string {
	if len(snap.list) == 0 {
		return "（无）"
	}
	names := make([]string, 0, len(snap.list))
	for _, g := range snap.list {
		names = append(names, "/@img/"+g.Name)
	}
	return strings.Join(names, "、")
}

var _ = context.Background
var _ = html.EscapeString
