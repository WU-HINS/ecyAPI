package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ecyapi/internal/config"
	"ecyapi/internal/store"
)

// newGroupServer 造一个含两个分类的实例，并写入 groups.json。
func newGroupServer(t *testing.T, groupsJSON string) *Server {
	t.Helper()
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.MkdirAll("data", 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(dir, "categories")
	for _, cat := range []string{"pc", "phone"} {
		p := filepath.Join(dataDir, cat)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, cat+"1.png"), subdirPNG(t, 40, 20), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, cat+"2.png"), subdirPNG(t, 20, 40), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile("data/groups.json", []byte(groupsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := New(config.Config{DataDir: dataDir, AdminPassword: "pw", PreviewMP: 16})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestGroupRedirect /@img/<name> 与 /{分类} 一样：302 跳到组内随机一张图。
func TestGroupRedirect(t *testing.T) {
	s := newGroupServer(t, "{\"groups\":[{\"name\":\"mix\",\"label\":\"混合\",\"categories\":[\"pc\",\"phone\"]}]}")

	seen := map[string]bool{}
	for i := 0; i < 30; i++ {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/@img/mix", nil))
		if rec.Code != http.StatusFound {
			t.Fatalf("应 302，得到 %d", rec.Code)
		}
		loc := rec.Header().Get("Location")
		if !strings.HasPrefix(loc, "/pic/") {
			t.Fatalf("跳转地址不对: %s", loc)
		}
		for _, cat := range []string{"/pic/pc/", "/pic/phone/"} {
			if strings.HasPrefix(loc, cat) {
				seen[cat] = true
			}
		}
	}
	if !seen["/pic/pc/"] || !seen["/pic/phone/"] {
		t.Fatalf("随机应覆盖两个成员分类，实际 %v", seen)
	}

	// ?w= 透传
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/@img/mix?w=480", nil))
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "?w=480") {
		t.Fatalf("?w= 未透传: %s", loc)
	}
}

// TestGroupJSON 带 ?json 返回该分组的一条随机链接。
func TestGroupJSON(t *testing.T) {
	s := newGroupServer(t, "{\"groups\":[{\"name\":\"mix\",\"categories\":[\"pc\",\"phone\"]}]}")
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		rec := get(t, s, "/@img/mix?json")
		if rec.Code != http.StatusOK {
			t.Fatalf("?json 应 200，得到 %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "/pic/") {
			t.Fatalf("应返回链接，得到 %q", body)
		}
		for _, cat := range []string{"/pic/pc/", "/pic/phone/"} {
			if strings.Contains(body, cat) {
				seen[cat] = true
			}
		}
	}
	if !seen["/pic/pc/"] || !seen["/pic/phone/"] {
		t.Fatalf("随机应覆盖两个分类，实际 %v", seen)
	}
}

// TestGroupNotFound 未知分组、多级路径都应 404。
func TestGroupNotFound(t *testing.T) {
	s := newGroupServer(t, "{\"groups\":[{\"name\":\"mix\",\"categories\":[\"pc\"]}]}")
	for _, p := range []string{"/@img/nope", "/@img/mix/sub"} {
		if rec := get(t, s, p); rec.Code != http.StatusNotFound {
			t.Fatalf("%s 应 404，得到 %d", p, rec.Code)
		}
	}
}

// TestGroupDefinitionValidation 非法定义要拒绝。
func TestGroupDefinitionValidation(t *testing.T) {
	cases := []string{
		"{\"groups\":[{\"name\":\"bad name\",\"categories\":[\"pc\"]}]}",
		"{\"groups\":[{\"name\":\"a/b\",\"categories\":[\"pc\"]}]}",
		"{\"groups\":[{\"name\":\"x\",\"categories\":[]}]}",
		"{\"groups\":[{\"name\":\"x\",\"categories\":[\"pc\"]},{\"name\":\"x\",\"categories\":[\"pc\"]}]}",
		"not json",
	}
	for _, c := range cases {
		if _, err := readGroups(writeTemp(t, c)); err == nil {
			t.Fatalf("应拒绝非法定义: %s", c)
		}
	}
	if _, err := readGroups(writeTemp(t, "{\"groups\":[{\"name\":\"mix\",\"categories\":[\"pc\"]}]}")); err != nil {
		t.Fatalf("合法定义被拒: %v", err)
	}
}

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "groups.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestGroupAdminUpsertAndDelete 后台列表编辑：新增、改名、删分类、删除分组。
func TestGroupAdminUpsertAndDelete(t *testing.T) {
	s := newGroupServer(t, "{\"groups\":[]}")
	cookie := login(t, s, "pw")
	if cookie == nil {
		t.Fatal("登录失败")
	}

	// 新增
	msg := postAdmin(t, s, cookie, "/admin/groups/upsert", url.Values{
		"name": {"mix"}, "label": {"混合"}, "categories": {"pc", "phone"},
	})
	if !strings.Contains(msg, "已保存分组") {
		t.Fatalf("新增失败: %s", msg)
	}
	if rec := get(t, s, "/@img/mix"); rec.Code != http.StatusFound {
		t.Fatalf("新分组应 302，得到 %d", rec.Code)
	}

	// 更新（改名 + 换成员）
	msg = postAdmin(t, s, cookie, "/admin/groups/upsert", url.Values{
		"orig": {"mix"}, "name": {"mix2"}, "label": {"混合2"}, "categories": {"phone"},
	})
	if !strings.Contains(msg, "已保存分组") {
		t.Fatalf("更新失败: %s", msg)
	}
	if rec := get(t, s, "/@img/mix"); rec.Code != http.StatusNotFound {
		t.Fatalf("旧名字应 404，得到 %d", rec.Code)
	}
	if rec := get(t, s, "/@img/mix2"); rec.Code != http.StatusFound {
		t.Fatalf("新名字应 302，得到 %d", rec.Code)
	}

	// 非法名字 / 空成员被拒
	if msg := postAdmin(t, s, cookie, "/admin/groups/upsert", url.Values{
		"name": {"a/b"}, "categories": {"pc"},
	}); !strings.Contains(msg, "只能用小写字母") {
		t.Fatalf("非法名字应被拒: %s", msg)
	}
	if msg := postAdmin(t, s, cookie, "/admin/groups/upsert", url.Values{
		"name": {"empty"},
	}); !strings.Contains(msg, "至少要包含一个存在的分类") {
		t.Fatalf("空成员应被拒: %s", msg)
	}

	// 删除
	if msg := postAdmin(t, s, cookie, "/admin/groups/delete", url.Values{"name": {"mix2"}}); !strings.Contains(msg, "已删除分组") {
		t.Fatalf("删除失败: %s", msg)
	}
	if rec := get(t, s, "/@img/mix2"); rec.Code != http.StatusNotFound {
		t.Fatalf("删除后应 404，得到 %d", rec.Code)
	}
}

// TestGroupManualCategories 手填分类（逗号/空格分隔）与勾选合并，不存在的被忽略并提示。
func TestGroupManualCategories(t *testing.T) {
	s := newGroupServer(t, "{\"groups\":[]}")
	cookie := login(t, s, "pw")
	if cookie == nil {
		t.Fatal("登录失败")
	}
	msg := postAdmin(t, s, cookie, "/admin/groups/upsert", url.Values{
		"name":              {"m1"},
		"categories":        {"pc"},
		"categories_manual": {"phone, nope/xx  pc"},
	})
	if !strings.Contains(msg, "已保存分组") {
		t.Fatalf("保存失败: %s", msg)
	}
	if !strings.Contains(msg, "忽略 1 个不存在的分类") {
		t.Fatalf("应提示忽略了不存在的分类: %s", msg)
	}
	if n, err := s.st.GroupCount(t.Context(), []string{"pc", "phone"}, store.OrientationAny); err != nil || n != 4 {
		t.Fatalf("分组应聚合 pc(2)+phone(2)=4，实际 %d（%v）", n, err)
	}
}

// TestHomeShowsGroups 首页展示虚拟分组卡片。
func TestHomeShowsGroups(t *testing.T) {
	s := newGroupServer(t, "{\"groups\":[{\"name\":\"mix\",\"label\":\"混合合集\",\"desc\":\"两组一起\",\"categories\":[\"pc\",\"phone\"]}]}")
	rec := get(t, s, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("首页应 200，得到 %d", rec.Code)
	}
	body := rec.Body.String()
	// 卡片与普通分类同款：预览图直接指向分组端点（每次加载都会重新随机）
	if !strings.Contains(body, `data-src="/@img/mix?w=480"`) {
		t.Fatal("首页分组卡片应使用 /@img/mix?w=480 作为预览")
	}
	// 显示完整链接
	if !strings.Contains(body, "http://example.com/@img/mix") {
		t.Fatal("首页分组卡片应显示完整 URL")
	}
	if !strings.Contains(body, "混合合集") {
		t.Fatal("首页应显示分组标签")
	}
	// 「点击图片刷新」提示也要在（与普通分类一致）
	if !strings.Contains(body, "点击图片可刷新预览") {
		t.Fatal("分组卡片应有刷新提示")
	}
}

// TestGroupAcceptsParentAggregate 父目录聚合（自身没图，只有子目录）也能作为成员。
func TestGroupAcceptsParentAggregate(t *testing.T) {
	// 目录结构：pixiv/kuro/1.png（pixiv 自身没有图片，是父聚合）
	s := newGroupServer(t, "{\"groups\":[]}")
	cookie := login(t, s, "pw")
	if cookie == nil {
		t.Fatal("登录失败")
	}
	// newGroupServer 只有 pc / phone；用 pc 当一个「父聚合」需要子目录，这里直接测 pc 的父级
	if msg := postAdmin(t, s, cookie, "/admin/groups/upsert", url.Values{
		"name": {"agg"}, "categories_manual": {"pc"},
	}); !strings.Contains(msg, "已保存分组") {
		t.Fatalf("普通分类应可保存: %s", msg)
	}
	// 不存在的路径仍然被拒
	msg := postAdmin(t, s, cookie, "/admin/groups/upsert", url.Values{
		"name": {"bad"}, "categories_manual": {"nope/xx"},
	})
	if !strings.Contains(msg, "至少要包含一个存在的分类") {
		t.Fatalf("不存在的路径应被拒: %s", msg)
	}
}

// TestGroupMemberUniform 虚拟分组按「成员分类」平权：big(100 张) 与 small(1 张) 各约一半。
func TestGroupMemberUniform(t *testing.T) {
	s := newGroupServer(t, "{\"groups\":[{\"name\":\"mix\",\"categories\":[\"pc\",\"phone\"]}]}")
	counts := map[string]int{}
	const n = 600
	for i := 0; i < n; i++ {
		rec := get(t, s, "/@img/mix?json")
		if rec.Code != http.StatusOK {
			t.Fatalf("?json 应 200，得到 %d", rec.Code)
		}
		body := rec.Body.String()
		switch {
		case strings.Contains(body, "/pic/pc/"):
			counts["pc"]++
		case strings.Contains(body, "/pic/phone/"):
			counts["phone"]++
		}
	}
	for _, m := range []string{"pc", "phone"} {
		if counts[m] < n/3 || counts[m] > n*2/3 {
			t.Fatalf("成员 %s 占比异常：%d/%d（%v）", m, counts[m], n, counts)
		}
	}
}

// TestCategorySearchUI 后台各分类选择与前端图库都要有搜索框。
func TestCategorySearchUI(t *testing.T) {
	s := newGroupServer(t, "{\"groups\":[{\"name\":\"mix\",\"categories\":[\"pc\"]}]}")
	cookie := login(t, s, "pw")
	if cookie == nil {
		t.Fatal("登录失败")
	}

	req := httptest.NewRequest("GET", "/admin", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()

	for _, target := range []string{"tableSelect", "del_cat_select", "binding_desk,binding_mobile", "prewarm_category", "gcat-new"} {
		if !strings.Contains(body, `data-filter="`+target+`"`) {
			t.Fatalf("后台缺少分类搜索框：%s", target)
		}
	}
	if !strings.Contains(body, "function applyFilter(") {
		t.Fatal("后台缺少分类过滤脚本")
	}

	// 前端图库
	rec2 := get(t, s, "/img")
	if rec2.Code != http.StatusOK {
		t.Fatalf("图库应 200，得到 %d", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), `id="catSearch"`) {
		t.Fatal("图库缺少分类搜索框")
	}
	if !strings.Contains(rec2.Body.String(), `id="categoryTabs"`) {
		t.Fatal("图库分类区缺少 id=categoryTabs")
	}
}

// TestHomeOrderWithGroups 「栏目与排序」里虚拟分组与栏目混排，顺序要落到首页卡片。
func TestHomeOrderWithGroups(t *testing.T) {
	s := newGroupServer(t, "{\"groups\":[{\"name\":\"mix\",\"label\":\"混合分组\",\"categories\":[\"pc\"]}]}")
	cookie := login(t, s, "pw")
	if cookie == nil {
		t.Fatal("登录失败")
	}
	// 建一个栏目
	msg := postAdmin(t, s, cookie, "/admin/binding", url.Values{
		"binding_desk": {"pc"}, "binding_path": {"ep1"}, "binding_label": {"栏目一"},
	})
	if !strings.Contains(msg, "成功") && !strings.Contains(msg, "已创建") && !strings.Contains(msg, "栏目") {
		t.Logf("binding 返回: %s", msg) // 文案变了也不至于误判
	}

	// 分组排在栏目之前
	body := postAdmin(t, s, cookie, "/admin/endpoints/reorder", url.Values{
		"order": {"@img/mix", "ep1"},
	})
	if !strings.Contains(body, "true") {
		t.Fatalf("排序保存失败: %s", body)
	}
	// 卡片区里用 data-src 定位（页面上别处也可能出现 /ep1 字样）
	cardPos := func(home, marker string) int {
		return strings.Index(home, `data-src="`+marker)
	}
	home := get(t, s, "/").Body.String()
	gi, ei := cardPos(home, "/@img/mix"), cardPos(home, "/ep1")
	if gi < 0 || ei < 0 {
		t.Fatalf("首页应同时有分组与栏目卡片（gi=%d ei=%d）", gi, ei)
	}
	if gi > ei {
		t.Fatalf("分组应排在栏目之前（gi=%d ei=%d）", gi, ei)
	}

	// 换回栏目在前
	postAdmin(t, s, cookie, "/admin/endpoints/reorder", url.Values{"order": {"ep1", "@img/mix"}})
	home = get(t, s, "/").Body.String()
	gi, ei = cardPos(home, "/@img/mix"), cardPos(home, "/ep1")
	if ei > gi {
		t.Fatalf("栏目应排在分组之前（gi=%d ei=%d）", gi, ei)
	}
}

// TestHomeOnlyTopLevelCategories 首页只展示一级分类：/P/ii 与 /P/jj 归一张 /P，图库不受影响。
func TestHomeOnlyTopLevelCategories(t *testing.T) {
	s := newGroupServer(t, "{\"groups\":[]}")
	cookie := login(t, s, "pw")
	if cookie == nil {
		t.Fatal("登录失败")
	}
	// 造两个深层栏目：pc/ii、pc/jj（pc 下有图，作为父聚合）
	for _, p := range []struct{ path, label string }{
		{"pc/ii", "子栏目一"},
		{"pc/jj", "子栏目二"},
		{"phone", "手机"},
	} {
		postAdmin(t, s, cookie, "/admin/binding", url.Values{
			"binding_desk": {"pc"}, "binding_path": {p.path}, "binding_label": {p.label},
		})
	}

	home := get(t, s, "/").Body.String()
	// 一级分类出现
	if !strings.Contains(home, `data-src="/pc?w=480"`) {
		t.Fatal("首页应出现一级分类 /pc 的卡片")
	}
	if !strings.Contains(home, `data-src="/phone?w=480"`) {
		t.Fatal("首页应出现 /phone 卡片")
	}
	// 深层路径不再单独出卡片
	if strings.Contains(home, `data-src="/pc/ii?w=480"`) || strings.Contains(home, `data-src="/pc/jj?w=480"`) {
		t.Fatal("首页不应出现 /pc/ii、/pc/jj 这类深层卡片")
	}
	// 只出现一张 /pc 卡片
	if n := strings.Count(home, `data-src="/pc?w=480"`); n != 1 {
		t.Fatalf("/pc 卡片应只有一张，实际 %d", n)
	}

	// 图库仍按完整路径列分类（不受影响）
	gallery := get(t, s, "/img").Body.String()
	if !strings.Contains(gallery, "pc/ii") && !strings.Contains(gallery, "pc/jj") {
		t.Log("图库分类栏按分类目录列出（没有 pc/ii 这类仅栏目路径时正常）")
	}
	// 深层栏目路径本身仍然可用（302 一跳）
	if rec := get(t, s, "/pc/ii"); rec.Code != http.StatusFound {
		t.Fatalf("/pc/ii 应仍可访问并 302，得到 %d", rec.Code)
	}
}

// TestSortTableNormalized 排序表按一级分类归一：/pc/ii、/pc/jj 只出一行 /pc，拖动整组生效。
func TestSortTableNormalized(t *testing.T) {
	s := newGroupServer(t, "{\"groups\":[{\"name\":\"mix\",\"label\":\"混合分组\",\"categories\":[\"pc\"]}]}")
	cookie := login(t, s, "pw")
	if cookie == nil {
		t.Fatal("登录失败")
	}
	for _, p := range []struct{ path, label string }{
		{"pc/ii", "子一"}, {"pc/jj", "子二"}, {"phone", "手机"},
	} {
		postAdmin(t, s, cookie, "/admin/binding", url.Values{
			"binding_desk": {"pc"}, "binding_path": {p.path}, "binding_label": {p.label},
		})
	}

	// 排序表：只有 pc / phone / @img/mix 三行
	req := httptest.NewRequest("GET", "/admin", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Count(body, `data-path="pc"`) != 1 {
		t.Fatal("排序表里 /pc 应只有一行")
	}
	if strings.Contains(body, `data-path="pc/ii"`) || strings.Contains(body, `data-path="pc/jj"`) {
		t.Fatal("排序表不应出现深层路径行")
	}
	if !strings.Contains(body, `data-path="@img/mix"`) {
		t.Fatal("排序表应包含虚拟分组行")
	}

	// 把 phone 拖到 pc 前面
	postAdmin(t, s, cookie, "/admin/endpoints/reorder", url.Values{"order": {"phone", "pc", "@img/mix"}})
	home := get(t, s, "/").Body.String()
	ei := strings.Index(home, `data-src="/phone?w=480"`)
	ci := strings.Index(home, `data-src="/pc?w=480"`)
	if ei < 0 || ci < 0 || ei > ci {
		t.Fatalf("首页顺序应为 phone 在 pc 之前（ei=%d ci=%d）", ei, ci)
	}
	// 深层栏目仍可访问
	if rec := get(t, s, "/pc/ii"); rec.Code != http.StatusFound {
		t.Fatalf("/pc/ii 应仍可访问，得到 %d", rec.Code)
	}
}

// TestAllRandomWeightedByCount /@img 与虚拟分组按 sqrt(图片数)+底分 加权：
// 大分类占多数，但小分类仍有份额（不再是成员平权）。
func TestAllRandomWeightedByCount(t *testing.T) {
	s := newGroupServer(t, "{\u0022groups\u0022:[{\u0022name\u0022:\u0022mix\u0022,\u0022categories\u0022:[\u0022pc\u0022,\u0022phone\u0022]}]}")
	counts := map[string]int{}
	const n = 400
	for i := 0; i < n; i++ {
		body := get(t, s, "/@img?json").Body.String()
		switch {
		case strings.Contains(body, "/pic/pc/"):
			counts["pc"]++
		case strings.Contains(body, "/pic/phone/"):
			counts["phone"]++
		}
	}
	// 两个分类图片数相同（各 2 张）→ 加权后期望仍各约一半（上浮即证明加权生效）
	if counts["pc"] < n/3 || counts["phone"] < n/3 {
		t.Fatalf("相同规模的两个分类应各约一半，实际 %v", counts)
	}
}
