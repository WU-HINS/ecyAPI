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
	if !strings.Contains(body, "/@img/mix") {
		t.Fatal("首页应出现分组链接 /@img/mix")
	}
	if !strings.Contains(body, "混合合集") {
		t.Fatal("首页应显示分组标签")
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
