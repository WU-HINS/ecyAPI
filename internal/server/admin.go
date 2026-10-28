package server

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ecyapi/internal/store"
)

// handleAdmin 后台：登录 + 图片管理 + 新建分类 + 双端绑定。
func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Query().Get("logout") != "":
		s.sessionEnd(w, r)
		http.Redirect(w, r, "/admin", http.StatusFound)
		return

	case r.Method == http.MethodPost && r.PostFormValue("login_password") != "":
		ip := clientIP(r)
		if blocked, wait := s.login.blocked(ip); blocked {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintf(w, "登录尝试过于频繁，请 %d 分钟后再试。", int(wait.Minutes())+1)
			return
		}
		input := strings.TrimSpace(r.PostFormValue("login_password"))
		if subtle.ConstantTimeCompare([]byte(input), []byte(s.cfg.AdminPassword)) == 1 {
			s.login.reset(ip)
			s.sessionStart(w, r)
			http.Redirect(w, r, "/admin", http.StatusFound)
			return
		}
		s.login.fail(ip)
		s.renderAdmin(w, map[string]any{"Authed": false, "LoginError": true})
		return

	case !s.sessionValid(r):
		s.renderAdmin(w, map[string]any{"Authed": false})
		return
	}

	tables := s.sortedCategories(r.Context())
	counts := s.categoryCountMap(r.Context())
	hidden := s.galleryHiddenSet()
	type cat struct {
		Name  string
		Count int
		Hide  bool
	}
	catList := make([]cat, 0, len(tables))
	for _, t := range tables {
		catList = append(catList, cat{Name: t, Count: counts[t], Hide: hidden[t]})
	}

	// 栏目表格：Mode 取值 单端 / 双端 / 自动横竖。
	type epItem struct {
		Path, Label, Desk, Mobile, Mode string
	}
	s.mu.RLock()
	epList := make([]epItem, 0, len(s.endpoints))
	for _, ep := range s.endpoints {
		mode := "单端"
		switch {
		case ep.auto:
			mode = "自动横竖"
		case ep.mobile != "":
			mode = "双端"
		}
		epList = append(epList, epItem{ep.path, ep.label, ep.desk, ep.mobile, mode})
	}
	s.mu.RUnlock()

	s.mu.RLock()
	baseURL := s.baseURL
	slogan, share, icp, icpURL := s.slogan, s.share, s.icp, s.icpURL
	logs := append([]string(nil), s.logs...)
	s.mu.RUnlock()

	s.renderAdmin(w, map[string]any{
		"Authed":       true,
		"Tables":       catList,
		"Endpoints":    epList,
		"ScanWarnings": store.ScanWarnings(s.st),
		"BaseURL":      baseURL,
		"Slogan":       slogan,
		"Share":        share,
		"ICP":          icp,
		"ICPURL":       icpURL,
		"Logs":         logs,
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
	// 批量上传的单个请求体可达上百 MB，公网上行传输常超过全局 ReadTimeout，
	// 这里按请求放宽读超时；ReadHeaderTimeout 仍在连接层防慢连接攻击。
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	// 请求体总量上限：超时放开后必须靠这个兜底，防止单个请求吃满磁盘。
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes)
	// 先解析再取字段：请求体被 MaxBytesReader 拦下时，FormValue 会拿到空值，
	// 那样用户只会看到「分类名不合法」，掩盖真正的原因（请求体过大）。
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		s.adminResult(w, "解析上传失败: "+err.Error())
		return
	}
	category := strings.TrimSpace(r.FormValue("category"))
	if !storeIsValid(category) {
		s.adminResult(w, "分类名不合法")
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
	// 同步清理被删图片的压缩预览缓存。
	for _, name := range names {
		s.cleanupPreviewCache(category, name)
	}
	s.adminResult(w, fmt.Sprintf("已删除 %d 张图片", n))
}

// handleAdminCreate 新建分类目录，并同步创建首页栏目（卡片 + /分类 跳转接口）。
func (s *Server) handleAdminCreate(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) || s.admin == nil {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	name := strings.TrimSpace(r.PostFormValue("new_table_name"))
	label := strings.TrimSpace(r.PostFormValue("new_table_label"))
	desc := strings.TrimSpace(r.PostFormValue("new_table_desc"))
	if hasReservedTop(name) {
		s.adminResult(w, "分类名不能以 "+strings.SplitN(name, "/", 2)[0]+" 开头（与内置接口冲突）")
		return
	}
	if !store.IsValidCategory(name) {
		s.adminResult(w, "分类名不合法：不能为空、不能以 . 开头、不能含 / ? # % 或控制字符；多级用 / 分隔，最多 3 段，每段 ≤128 字节")
		return
	}
	if err := s.admin.CreateCategory(r.Context(), name); err != nil {
		// 目录已存在时继续补齐首页栏目，兼容此前只建了目录的旧数据。
		if !strings.Contains(err.Error(), "已存在") {
			s.adminResult(w, "创建失败: "+err.Error())
			return
		}
	}
	// 默认按设备自动分横竖：桌面端取横图、移动端取竖图，无需第二个分类。
	// 表单里「关闭自动横竖」勾选后提交 new_table_auto=0，才退回单端全量。
	auto := r.PostFormValue("new_table_auto") != "0"
	var addErr error
	if auto {
		addErr = s.addAdaptive(name, name, label, desc)
	} else {
		addErr = s.addEndpoint(name, name, "", label, desc)
	}
	if addErr != nil {
		if !strings.Contains(addErr.Error(), "已存在") {
			s.adminResult(w, "分类创建成功，但首页栏目添加失败: "+addErr.Error())
			return
		}
	}
	if auto {
		s.adminResult(w, "分类 "+name+" 创建成功，栏目 /"+name+" 已上线（按设备自动分横竖：桌面出横图、移动端出竖图）。")
		return
	}
	s.adminResult(w, "分类 "+name+" 创建成功，首页栏目 /"+name+" 已同步上线（不区分设备）！")
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
	// 自动横竖：只用一个分类，桌面端出横图、移动端出竖图，无需绑定第二个分类。
	// 表单默认开启；勾选「关闭自动横竖」时提交 binding_auto=0，改用移动端分类。
	if r.PostFormValue("binding_auto") != "0" {
		if err := s.addAdaptive(path, desk, label, desc); err != nil {
			s.adminResult(w, "创建失败: "+err.Error())
			return
		}
		s.adminResult(w, "自动横竖栏目 /"+path+" 创建成功：桌面端取横图，移动端取竖图（同一分类 "+desk+"）。")
		return
	}
	if err := s.addBinding(path, desk, mobile, label, desc); err != nil {
		s.adminResult(w, "创建失败: "+err.Error())
		return
	}
	s.adminResult(w, "双端栏目 /"+path+" 创建成功！")
}

// handleAdminSettings 保存站点设置：对外 API 域名。
func (s *Server) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	raw := strings.TrimSpace(r.PostFormValue("base_url"))
	if raw != "" && normalizeBaseURL(raw) == "" {
		s.adminResult(w, "域名格式不正确，示例：https://api.example.com")
		return
	}
	if err := s.setBaseURL(raw); err != nil {
		s.adminResult(w, "保存失败: "+err.Error())
		return
	}
	if normalizeBaseURL(raw) == "" {
		s.adminResult(w, "已清空域名设置，链接将按当前请求自动推断。")
		return
	}
	s.adminResult(w, "域名已保存，API 输出链接将使用 "+normalizeBaseURL(raw))
}

// handleAdminContent 保存首页文案：slogan / 分享语 / ICP 备案号文字与链接。
func (s *Server) handleAdminContent(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	slogan := r.PostFormValue("slogan")
	share := r.PostFormValue("share")
	icp := r.PostFormValue("icp")
	icpURL := r.PostFormValue("icp_url")
	if err := s.setContent(slogan, share, icp, icpURL); err != nil {
		s.adminResult(w, "保存失败: "+err.Error())
		return
	}
	s.adminResult(w, "首页文案已保存。")
}

// handleAdminLogAdd 新增一条改动日志（插入到最顶部）。
func (s *Server) handleAdminLogAdd(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	if err := s.addLog(r.PostFormValue("log_text")); err != nil {
		s.adminResult(w, "添加失败: "+err.Error())
		return
	}
	s.adminResult(w, "改动日志已添加。")
}

// handleAdminLogDelete 按序号删除一条改动日志。
func (s *Server) handleAdminLogDelete(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	index, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("log_index")))
	if err != nil {
		s.adminResult(w, "序号不合法")
		return
	}
	if err := s.deleteLog(index); err != nil {
		s.adminResult(w, "删除失败: "+err.Error())
		return
	}
	s.adminResult(w, "改动日志已删除。")
}

// handleAdminEndpointsReorder 拖拽排序保存：接收完整栏目顺序并整体重排。
func (s *Server) handleAdminEndpointsReorder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if !s.sessionValid(r) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"ok":false,"msg":"未授权"}`)
		return
	}
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"ok":false,"msg":"参数错误"}`)
		return
	}
	order := r.Form["order"]
	if len(order) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"ok":false,"msg":"顺序为空"}`)
		return
	}
	if err := s.reorderEndpoints(order); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"ok":false,"msg":%q}`, "保存失败: "+err.Error())
		return
	}
	fmt.Fprint(w, `{"ok":true}`)
}

// handleAdminGalleryVisibility 切换分类在图库页的显示/隐藏。
func (s *Server) handleAdminGalleryVisibility(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.adminResult(w, "参数错误")
		return
	}
	category := strings.TrimSpace(r.PostFormValue("category"))
	if !storeIsValid(category) {
		s.adminResult(w, "分类名不合法")
		return
	}
	// 分类须真实存在，防止开关一个不存在的名字
	if _, err := s.st.Count(r.Context(), category); err != nil {
		s.adminResult(w, "分类不存在")
		return
	}
	hidden, err := s.toggleGalleryHidden(category)
	if err != nil {
		s.adminResult(w, "保存失败: "+err.Error())
		return
	}
	if hidden {
		s.adminResult(w, "分类 "+category+" 已设置为不在图库展示。")
	} else {
		s.adminResult(w, "分类 "+category+" 已恢复在图库展示。")
	}
}

// handleAdminCategoryDelete 删除整个分类目录（含全部图片），并同步下线引用该分类的栏目。
// 须携带 confirm_delete=1 才执行，防止误触。
func (s *Server) handleAdminCategoryDelete(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) || s.admin == nil {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.adminResult(w, "参数错误")
		return
	}
	category := strings.TrimSpace(r.PostFormValue("category"))
	if !storeIsValid(category) {
		s.adminResult(w, "分类名不合法")
		return
	}
	if r.PostFormValue("confirm_delete") != "1" {
		s.adminResult(w, "请先勾选确认复选框再删除")
		return
	}
	// 先统计图片数量用于结果提示
	count, _ := s.st.Count(r.Context(), category)
	// 下线引用该分类的首页栏目
	removed, err := s.removeEndpointsByCategory(category)
	if err != nil {
		s.adminResult(w, "栏目下线失败: "+err.Error())
		return
	}
	// 删除分类目录及全部图片，并同步清理该分类的压缩预览缓存
	if err := s.admin.DeleteCategory(r.Context(), category); err != nil {
		s.adminResult(w, "删除失败: "+err.Error())
		return
	}
	s.cleanupPreviewCache(category, "")
	// 顺带清掉图库隐藏列表中该分类的残留记录
	if err := s.pruneGalleryHidden(s.sortedCategories(r.Context())); err != nil {
		s.adminResult(w, "分类已删除，但清理图库隐藏记录失败: "+err.Error())
		return
	}
	msg := fmt.Sprintf("分类 %s 已删除（含 %d 张图片）。", category, count)
	if len(removed) > 0 {
		msg += "已同步下线关联栏目：" + strings.Join(removed, "、")
	} else {
		msg += "没有栏目引用该分类。"
	}
	s.adminResult(w, msg)
}

// handleAdminUpdate 旧接口保留：不再支持按链接编辑，提示使用图片管理。
func (s *Server) handleAdminUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.sessionValid(r) {
		http.Error(w, "未授权", http.StatusUnauthorized)
		return
	}
	s.adminResult(w, "链接编辑已停用，请使用图片上传/删除管理。")
}

// adminDataPageSize 后台图片预览每页数量。
const adminDataPageSize = 40

// adminDataPage 后台预览分页响应。
type adminDataPage struct {
	Items      []store.Row `json:"items"`
	Page       int         `json:"page"`
	PageSize   int         `json:"page_size"`
	Total      int         `json:"total"`
	TotalPages int         `json:"total_pages"`
}

// handleAdminData 返回某分类图片列表（JSON，分页输出，供后台预览加载）。
// 查询参数：table 分类名，page 页码（默认 1，从 1 开始）。
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
	// 先取总数（同时判断分类是否存在），再只取当前页，避免大分类整表复制。
	total, err := s.st.CountWith(r.Context(), table, store.OrientationAny)
	if err != nil {
		http.Error(w, "Table not found.", http.StatusNotFound)
		return
	}
	totalPages := (total + adminDataPageSize - 1) / adminDataPageSize
	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || page < 1 {
		page = 1
	}
	if totalPages > 0 && page > totalPages {
		page = totalPages
	}
	items, _, err := s.st.LinksPage(r.Context(), table, (page-1)*adminDataPageSize, adminDataPageSize)
	if err != nil {
		http.Error(w, "Table not found.", http.StatusNotFound)
		return
	}
	if items == nil {
		items = []store.Row{}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(adminDataPage{
		Items:      items,
		Page:       page,
		PageSize:   adminDataPageSize,
		Total:      total,
		TotalPages: totalPages,
	})
}

// storeIsValid 校验分类路径：后台表单与 store 层共用同一套规则
// （每段字母开头，仅字母/数字/下划线，最多 3 段，如 a/b），
// 避免后台放过、store 层再报 invalid category name 的口径不一致。
func storeIsValid(name string) bool { return store.IsValidCategory(name) }

func (s *Server) adminResult(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	plain := strings.NewReplacer("<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;")
	w.Write([]byte(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>结果</title></head><body style="font-family:sans-serif;text-align:center;padding-top:60px"><p>` + plain.Replace(msg) + `</p><script>setTimeout(function(){location.href='/admin'},1200)</script></body></html>`))
}
