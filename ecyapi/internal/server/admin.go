package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// handleAdmin 后台：登录 + 表编辑 + 新建分类。
func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Query().Get("logout") != "":
		s.sessionEnd(w, r)
		http.Redirect(w, r, "/admin", http.StatusFound)
		return

	case r.Method == http.MethodPost && r.PostFormValue("login_password") != "":
		input := strings.TrimSpace(r.PostFormValue("login_password"))
		if subtle.ConstantTimeCompare([]byte(input), []byte(s.cfg.AdminPassword)) == 1 {
			s.sessionStart(w)
			http.Redirect(w, r, "/admin", http.StatusFound)
			return
		}
		s.renderAdmin(w, map[string]any{"Authed": false, "LoginError": true})
		return

	case !s.sessionValid(r):
		s.renderAdmin(w, map[string]any{"Authed": false})
		return
	}

	tables := s.sortedCategories(r.Context())
	counts := s.categoryCountMap(r.Context())
	type cat struct {
		Name  string
		Count int
	}
	catList := make([]cat, 0, len(tables))
	for _, t := range tables {
		catList = append(catList, cat{Name: t, Count: counts[t]})
	}
	s.renderAdmin(w, map[string]any{"Authed": true, "Tables": catList})
}

func (s *Server) renderAdmin(w http.ResponseWriter, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	if _, ok := data["Authed"]; !ok {
		data["Authed"] = s.sessionValid(&http.Request{Header: http.Header{}})
	}
	s.render(w, http.StatusOK, "admin.html", data)
}

// handleAdminUpdate 全量覆盖某分类链接。
func (s *Server) handleAdminUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) || s.admin == nil {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	table := strings.TrimSpace(r.PostFormValue("table"))
	content := r.PostFormValue("content")
	var links []string
	for _, line := range strings.Split(content, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			links = append(links, line)
		}
	}
	if err := s.admin.UpsertLinks(r.Context(), table, links); err != nil {
		s.adminResult(w, "更新失败: "+err.Error())
		return
	}
	s.adminResult(w, "表内容更新成功！")
}

// handleAdminCreate 新建分类。
func (s *Server) handleAdminCreate(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) || s.admin == nil {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	name := strings.TrimSpace(r.PostFormValue("new_table_name"))
	if err := s.admin.CreateTable(r.Context(), name); err != nil {
		s.adminResult(w, "创建失败: "+err.Error())
		return
	}
	s.adminResult(w, "分类 " + name + " 创建成功！")
}

// handleAdminData 返回某分类全部链接（纯文本，供后台编辑器加载）。
func (s *Server) handleAdminData(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	table := r.URL.Query().Get("table")
	if !storeIsValid(table) {
		http.Error(w, "Invalid table name.", http.StatusBadRequest)
		return
	}
	rows, err := s.st.Links(r.Context(), table)
	if err != nil {
		http.Error(w, "Table not found.", http.StatusNotFound)
		return
	}
	var b strings.Builder
	for _, row := range rows {
		b.WriteString(row.Link)
		b.WriteByte('\n')
	}
	plainText(w, b.String())
}

func storeIsValid(name string) bool {
	const maxLen = 64
	if len(name) < 1 || len(name) > maxLen {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

func (s *Server) adminResult(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	plain := strings.NewReplacer("<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;")
	w.Write([]byte(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>结果</title></head><body style="font-family:sans-serif;text-align:center;padding-top:60px"><p>` + plain.Replace(msg) + `</p><script>setTimeout(function(){location.href='/admin'},1200)</script></body></html>`))
}
