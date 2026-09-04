package server

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
)

// handleAdmin 后台：登录 + 图片管理 + 新建分类 + 双端绑定。
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

	type binding struct {
		Path, Label, Desk, Mobile string
	}
	s.mu.RLock()
	bindings := make([]binding, 0, len(s.endpoints))
	for _, ep := range s.endpoints {
		if ep.mobile != "" {
			bindings = append(bindings, binding{ep.path, ep.label, ep.desk, ep.mobile})
		}
	}
	s.mu.RUnlock()

	s.renderAdmin(w, map[string]any{
		"Authed":   true,
		"Tables":   catList,
		"Bindings": bindings,
	})
}

func (s *Server) renderAdmin(w http.ResponseWriter, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	if _, ok := data["Authed"]; !ok {
		data["Authed"] = false
	}
	s.render(w, http.StatusOK, "admin.html", data)
}

// handleAdminUpload 上传图片到分类（multipart，可多文件）。
func (s *Server) handleAdminUpload(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) || s.admin == nil {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	category := strings.TrimSpace(r.FormValue("category"))
	if !storeIsValid(category) {
		s.adminResult(w, "分类名不合法")
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		s.adminResult(w, "解析上传失败: "+err.Error())
		return
	}
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		s.adminResult(w, "未选择文件")
		return
	}
	saved := 0
	for _, fh := range files {
		src, err := fh.Open()
		if err != nil {
			continue
		}
		if _, err := s.admin.SaveFile(r.Context(), category, fh.Filename, src); err == nil {
			saved++
		}
		src.Close()
	}
	s.adminResult(w, fmt.Sprintf("成功上传 %d / %d 张图片到分类 %s", saved, len(files), category))
}

// handleAdminDelete 删除分类内图片（支持批量）。
func (s *Server) handleAdminDelete(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) || s.admin == nil {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	category := strings.TrimSpace(r.FormValue("category"))
	if !storeIsValid(category) {
		s.adminResult(w, "分类名不合法")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.adminResult(w, "参数错误")
		return
	}
	names := r.Form["files"]
	if len(names) == 0 {
		s.adminResult(w, "未选择要删除的图片")
		return
	}
	n, err := s.admin.DeleteFiles(r.Context(), category, names)
	if err != nil {
		s.adminResult(w, "删除失败: "+err.Error())
		return
	}
	s.adminResult(w, fmt.Sprintf("已删除 %d 张图片", n))
}

// handleAdminCreate 新建分类目录。
func (s *Server) handleAdminCreate(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) || s.admin == nil {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	name := strings.TrimSpace(r.PostFormValue("new_table_name"))
	if err := s.admin.CreateCategory(r.Context(), name); err != nil {
		s.adminResult(w, "创建失败: "+err.Error())
		return
	}
	s.adminResult(w, "分类 "+name+" 创建成功！")
}

// handleAdminBinding 新增双端自适应绑定栏目。
func (s *Server) handleAdminBinding(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	path := strings.TrimSpace(r.PostFormValue("binding_path"))
	desk := strings.TrimSpace(r.PostFormValue("binding_desk"))
	mobile := strings.TrimSpace(r.PostFormValue("binding_mobile"))
	label := strings.TrimSpace(r.PostFormValue("binding_label"))
	desc := strings.TrimSpace(r.PostFormValue("binding_desc"))
	if err := s.addBinding(path, desk, mobile, label, desc); err != nil {
		s.adminResult(w, "创建失败: "+err.Error())
		return
	}
	s.adminResult(w, "双端栏目 /"+path+" 创建成功！")
}

// handleAdminUpdate 旧接口保留：不再支持按链接编辑，提示使用图片管理。
func (s *Server) handleAdminUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	s.adminResult(w, "链接编辑已停用，请使用图片上传/删除管理。")
}

// handleAdminData 返回某分类图片列表（JSON，供后台预览加载）。
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
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	writeRowsJSON(w, rows)
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
