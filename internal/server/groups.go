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
	// 保持文件里的顺序（用户在「栏目与排序」里拖动过）；新分组追加在末尾
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

// handleGroup 处理 /@img/<name>：对外与 /{分类} 一致——默认 302 跳一张图，
// ?json / ?json=N / ?orientation= / ?w= 语义相同。
//
// 抽样口径与 /@img 相同：**成员分类之间平权**（先等概率选一个成员，再在它内部随机取图），
// 而不是在并集上按图片数量加权——否则大分类会把小分类淹没。
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

	if _, hasJSON := r.URL.Query()["json"]; hasJSON {
		limit := positiveParam(r, "quantity", 1, s.cfg.MaxLinks)
		if v := r.URL.Query().Get("json"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		// 单条随机走加权抽样（保留随机性）；多条按权重配额分配
		var rows []store.Row
		if limit == 1 {
			if row, ok := s.pickFromMembers(r, name, members, want); ok {
				rows = []store.Row{row}
			}
		} else {
			rows = s.rowsFromMembers(r, members, limit, want)
		}
		if len(rows) == 0 {
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

	row, ok := s.pickFromMembers(r, name, members, want)
	if !ok {
		plainError(w, "No links found.", http.StatusNotFound)
		return
	}
	redirectRandom(w, r, row.Link+suffix)
}

// pickFromMembers 在成员分类里平权随机：先随机挑一个成员，取不到再换下一个（最多 8 个）。
func (s *Server) pickFromMembers(r *http.Request, groupName string, members []string, want store.Orientation) (store.Row, bool) {
	if len(members) == 0 {
		return store.Row{}, false
	}
	// 成员之间同样按 sqrt(图片数)+底分 加权（与 /@img、父目录聚合口径一致）
	table := s.groupWeightTable(groupName, members)
	tried := make(map[string]bool, 8)
	tries := len(members)
	if tries > 8 {
		tries = 8
	}
	for i := 0; i < tries; i++ {
		m, ok := table.pick(tried)
		if !ok {
			break
		}
		tried[m] = true
		if row, ok := s.pickVerified(r, m, want); ok {
			return row, true
		}
	}
	if want != store.OrientationAny {
		return s.pickFromMembers(r, groupName, members, store.OrientationAny)
	}
	return store.Row{}, false
}

// rowsFromMembers 从成员分类里按权重分配配额后取图：
// 权重同样是 sqrt(图片数)+底分，与单条 302 的口径保持一致。
func (s *Server) rowsFromMembers(r *http.Request, members []string, limit int, want store.Orientation) []store.Row {
	if limit < 1 || len(members) == 0 {
		return nil
	}
	counts := s.countsFor(r.Context(), members)
	// 先按权重算出每个成员的配额（最大余数法，保证加起来正好 limit）
	type quota struct {
		cat    string
		share  int
		remain float64
	}
	totalW := 0.0
	qs := make([]quota, 0, len(members))
	for _, m := range members {
		w := store.UnitWeight(counts[m])
		if w <= 0 {
			continue
		}
		totalW += w
		qs = append(qs, quota{cat: m, remain: w})
	}
	if totalW <= 0 {
		return nil
	}
	assigned := 0
	for i := range qs {
		exact := float64(limit) * (qs[i].remain / totalW)
		qs[i].share = int(exact)
		assigned += qs[i].share
		qs[i].remain = exact - float64(qs[i].share)
	}
	// 余数补齐：按小数部分从大到小
	for assigned < limit {
		best, bestR := -1, -1.0
		for i := range qs {
			if qs[i].remain > bestR {
				best, bestR = i, qs[i].remain
			}
		}
		if best < 0 {
			break
		}
		qs[best].share++
		qs[best].remain = -1 // 本轮不再参与
		assigned++
	}

	rows := make([]store.Row, 0, limit)
	for _, q := range qs {
		if q.share <= 0 {
			continue
		}
		rs, err := s.st.RandomWith(r.Context(), q.cat, q.share, want)
		if err != nil {
			continue
		}
		rows = append(rows, rs...)
	}
	if len(rows) == 0 && want != store.OrientationAny {
		return s.rowsFromMembers(r, members, limit, store.OrientationAny)
	}
	return rows
}

// saveGroups 把分组列表原子写回 groups.json 并刷新内存快照。
func (s *Server) saveGroups(list []virtualGroup) error {
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

// reorderGroups 按给定名字顺序重排分组（未列出的保持原顺序追加在后）。
func (s *Server) reorderGroups(names []string) error {
	cur := s.groupsSnapshot()
	rank := make(map[string]int, len(names))
	for i, n := range names {
		rank[n] = i
	}
	list := append([]virtualGroup(nil), cur.list...)
	sort.SliceStable(list, func(i, j int) bool {
		ri, iok := rank[list[i].Name]
		rj, jok := rank[list[j].Name]
		switch {
		case iok && jok:
			return ri < rj
		case iok:
			return true
		case jok:
			return false
		}
		return false
	})
	return s.saveGroups(list)
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
