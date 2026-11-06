package server

// roots_admin.go —— 附加图库的后台管理（M4c）。
//
// POST /admin/roots ：表单提交完整列表（roots[i].name / roots[i].path），
// 保存到 data/roots.json 并立即让 store 重扫。

import (
	"net/http"
	"path/filepath"
	"strings"

	"ecyapi/internal/config"
	"ecyapi/internal/store"
)

func (s *Server) handleAdminRoots(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.adminResult(w, "表单解析失败")
		return
	}
	names := r.PostForm["name"]
	paths := r.PostForm["path"]
	out := make([]config.ExtraRoot, 0, len(names))
	seen := map[string]bool{}
	for i := 0; i < len(names) && i < len(paths); i++ {
		name := strings.TrimSpace(names[i])
		p := strings.TrimSpace(paths[i])
		if name == "" || p == "" {
			continue
		}
		if strings.ContainsAny(name, "/\\") {
			s.adminResult(w, "库名不能包含斜杠: "+name)
			return
		}
		if seen[name] {
			s.adminResult(w, "库名重复: "+name)
			return
		}
		seen[name] = true
		out = append(out, config.ExtraRoot{Name: name, Path: filepath.Clean(p)})
	}

	file := filepath.Join(filepath.Dir(s.cfg.DataDir), "roots.json")
	if err := config.SaveRootsFile(file, out); err != nil {
		s.adminResult(w, "保存失败: "+err.Error())
		return
	}
	roots := make([]store.Root, 0, len(out))
	for _, r2 := range out {
		roots = append(roots, store.Root{Name: r2.Name, Path: r2.Path})
	}
	if up, ok := s.st.(interface{ SetExtraRoots([]store.Root) }); ok {
		up.SetExtraRoots(roots)
	}
	// 保存成功后跳回设置页（#settings 让前台 JS 切到对应视图）
	http.Redirect(w, r, "/admin#settings", http.StatusSeeOther)
}
