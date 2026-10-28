package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"ecyapi/internal/config"
)

// TestJSONMultiLinkShape 多链接返回必须是合法 JSON（手写序列化容易漏掉数组括号）。
func TestJSONMultiLinkShape(t *testing.T) {
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
	cat := filepath.Join(dataDir, "c")
	if err := os.MkdirAll(cat, 0o755); err != nil {
		t.Fatal(err)
	}
	png := subdirPNG(t, 16, 9)
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(cat, "f"+strconv.Itoa(i)+".png"), png, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(config.Config{DataDir: dataDir, AdminPassword: "pw", MaxLinks: 50})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		target string
		count  int
		array  bool
	}{
		{"/json?c", 1, false},
		{"/json?c=2", 2, true},
		{"/json?c=9", 3, true}, // 请求 9 条但只有 3 张
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", c.target, nil))

		var payload struct {
			Code  int             `json:"code"`
			Count int             `json:"count"`
			Data  json.RawMessage `json:"data"`
			Links []string        `json:"links"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("%s 返回非法 JSON: %v\n%s", c.target, err, rec.Body.String())
		}
		if payload.Code != 200 || payload.Count != c.count {
			t.Fatalf("%s code/count = %d/%d，期望 200/%d", c.target, payload.Code, payload.Count, c.count)
		}
		isArray := len(payload.Data) > 0 && payload.Data[0] == '['
		if isArray != c.array {
			t.Fatalf("%s data 数组形态 = %v，期望 %v（%s）", c.target, isArray, c.array, payload.Data)
		}
		if c.array {
			var rows []struct {
				ID   string `json:"id"`
				Link string `json:"link"`
			}
			if err := json.Unmarshal(payload.Data, &rows); err != nil {
				t.Fatalf("%s data 解析失败: %v", c.target, err)
			}
			if len(rows) != c.count || len(payload.Links) != c.count {
				t.Fatalf("%s data/links 条数 = %d/%d，期望 %d", c.target, len(rows), len(payload.Links), c.count)
			}
		}
	}
}
