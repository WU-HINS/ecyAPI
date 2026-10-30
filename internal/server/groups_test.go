package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ecyapi/internal/config"
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

// TestGroupPage 虚拟分组页面能聚合成员分类的图片。
func TestGroupPage(t *testing.T) {
	s := newGroupServer(t, "{\"groups\":[{\"name\":\"mix\",\"label\":\"混合\",\"categories\":[\"pc\",\"phone\"]}]}")

	rec := get(t, s, "/@img/mix")
	if rec.Code != http.StatusOK {
		t.Fatalf("分组页应 200，得到 %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/pic/pc/") || !strings.Contains(body, "/pic/phone/") {
		t.Fatal("分组页应同时包含两个分类的图片")
	}
	if !strings.Contains(body, "混合") {
		t.Fatal("应显示分组标签")
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
