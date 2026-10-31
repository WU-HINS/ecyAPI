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

// handleGroup 处理 /@img/<name>：在一个虚拟分组的成员图片池里随机，
// 对外与 /{分类} 一致——默认 302 跳一张图，?json / ?json=N / ?orientation= / ?w= 语义相同。
//
// 注意：分组是「成员的图片并集」，按图片数量加权随机（不是先随机分类再取图），
// 这样成员大小差异很大时也不会被小分类稀释。
func (s *Server) handleGroup(w http.ResponseWriter, r *http.Request, name string) {
	g, ok := s.groupByName(name)
	if !ok {
		plainError(w, "No such group.", http.StatusNotFound)
		return
	}
	// 与 /@img 一致：图库隐藏的分类不参与
	hidden := s.galleryHiddenSet()
	members := make([]string, 0, len(g.Categories))
	for _, c := range g.Categories {
		if !hidden[c] {
			members = append(members, c)
		}
	}
	if len(members) == 0 {
		plainError(w, "No links found.", http.StatusNotFound)
		return
	}

	want := linkOrientation(r, endpoint{auto: true})
	suffix := ""
	if v := r.URL.Query().Get("w"); v != "" {
		suffix = "?w=" + url.QueryEscape(v)
	}

	// ?json 分支
	if _, hasJSON := r.URL.Query()["json"]; hasJSON {
		limit := positiveParam(r, "quantity", 1, s.cfg.MaxLinks)
		if v := r.URL.Query().Get("json"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		rows, err := s.st.GroupRandom(r.Context(), members, want, limit)
		if (err != nil || len(rows) == 0) && want != store.OrientationAny {
			rows, err = s.st.GroupRandom(r.Context(), members, store.OrientationAny, limit)
		}
		if err != nil || len(rows) == 0 {
			plainError(w, "No links found.", http.StatusNotFound)
			return
		}
		if len(rows) == 1 {
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

	row, ok := s.pickGroupVerified(r, members, want)
	if !ok {
		plainError(w, "No links found.", http.StatusNotFound)
		return
	}
	http.Redirect(w, r, row.Link+suffix, http.StatusFound)
}

// pickGroupVerified 从分组里取一条并确认文件还在；方向为空时回退不限方向。
func (s *Server) pickGroupVerified(r *http.Request, members []string, want store.Orientation) (store.Row, bool) {
	for attempt := 0; attempt < 3; attempt++ {
		rows, err := s.st.GroupRandom(r.Context(), members, want, 1)
		if err != nil || len(rows) == 0 {
			break
		}
		if s.verifyRow(r, rows[0]) {
			return rows[0], true
		}
	}
	if want != store.OrientationAny {
		return s.pickGroupVerified(r, members, store.OrientationAny)
	}
	return store.Row{}, false
}

// saveGroups 把分组列表原子写回 groups.json 并刷新内存快照。
func (s *Server) saveGroups(list []virtualGroup) error {
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	doc := groupsDoc{Groups: list}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(groupsFile), 0o755); err != nil {
		return err
	}
	tmp := groupsFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, groupsFile); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// 重新解析一次，保证内存里的快照与磁盘完全一致（含 hash）
	snap, err := readGroups(groupsFile)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.groups = snap
	s.mu.Unlock()
	return nil
}

// handleAdminGroupUpsert 新增或更新一个虚拟分组（后台列表表单）。
func (s *Server) handleAdminGroupUpsert(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	orig := strings.ToLower(strings.TrimSpace(r.PostFormValue("orig")))
	name := strings.ToLower(strings.TrimSpace(r.PostFormValue("name")))
	label := strings.TrimSpace(r.PostFormValue("label"))
	desc := strings.TrimSpace(r.PostFormValue("desc"))
	if name == "" {
		s.adminResult(w, "分组名不能为空")
		return
	}
	if !groupNameRe.MatchString(name) {
		s.adminResult(w, "分组名只能用小写字母/数字与 - _，最长 32，且不能带 /（单级）")
		return
	}
	// 接受「真实分类」与「父目录聚合」：像 /pixiv 这种自身没图、只有子目录的路径也能入选
	// 勾选 + 手填合并：手填支持逗号/空格/换行分隔，方便批量粘路径。
	raw := append([]string{}, r.PostForm["categories"]...)
	if manual := strings.TrimSpace(r.PostFormValue("categories_manual")); manual != "" {
		fields := strings.FieldsFunc(manual, func(r rune) bool {
			return r == ',' || r == '，' || r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '、'
		})
		raw = append(raw, fields...)
	}
	members := make([]string, 0, len(raw))
	ignored := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, c := range raw {
		c = strings.Trim(strings.TrimSpace(c), "/")
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		if !s.st.HasPath(r.Context(), c) {
			ignored = append(ignored, c)
			continue
		}
		members = append(members, c)
	}
	if len(members) == 0 {
		s.adminResult(w, "至少要包含一个存在的分类（勾选或手填都可以）")
		return
	}

	cur := s.groupsSnapshot()
	list := make([]virtualGroup, 0, len(cur.list)+1)
	for _, g := range cur.list {
		if g.Name == orig && orig != "" {
			continue // 被本次更新替换
		}
		if g.Name == name {
			s.adminResult(w, "分组名 "+name+" 已存在")
			return
		}
		list = append(list, g)
	}
	if label == "" {
		label = name
	}
	list = append(list, virtualGroup{Name: name, Label: label, Desc: desc, Categories: members})
	if err := s.saveGroups(list); err != nil {
		s.adminResult(w, "保存失败: "+err.Error())
		return
	}
	log.Printf("管理操作：保存虚拟分组 /@img/%s（%d 个分类）", name, len(members))
	msg := fmt.Sprintf("已保存分组 /@img/%s（%d 个分类）", name, len(members))
	if len(ignored) > 0 {
		msg += fmt.Sprintf("；忽略 %d 个不存在的分类：%s", len(ignored), joinCapped(ignored, 5))
	}
	s.adminResult(w, msg)
}

// handleAdminGroupDelete 删除一个虚拟分组。
func (s *Server) handleAdminGroupDelete(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.PostFormValue("name")))
	cur := s.groupsSnapshot()
	list := make([]virtualGroup, 0, len(cur.list))
	found := false
	for _, g := range cur.list {
		if g.Name == name {
			found = true
			continue
		}
		list = append(list, g)
	}
	if !found {
		s.adminResult(w, "分组不存在")
		return
	}
	if err := s.saveGroups(list); err != nil {
		s.adminResult(w, "删除失败: "+err.Error())
		return
	}
	log.Printf("管理操作：删除虚拟分组 /@img/%s", name)
	s.adminResult(w, "已删除分组 /@img/"+name)
}

// joinCapped 最多列 n 个名字，其余折叠成「等 N 个」。
func joinCapped(names []string, n int) string {
	if len(names) <= n {
		return strings.Join(names, "、")
	}
	return strings.Join(names[:n], "、") + fmt.Sprintf(" 等 %d 个", len(names))
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
