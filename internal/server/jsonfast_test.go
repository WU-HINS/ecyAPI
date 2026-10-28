package server

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestAppendJSONStringMatchesStdlib 校验手写转义与 encoding/json 完全一致，
// 覆盖控制字符、引号、反斜杠、非 ASCII、HTML 字符与 U+2028。
func TestAppendJSONStringMatchesStdlib(t *testing.T) {
	cases := []string{
		"plain.png",
		"/pic/pc/a.webp",
		"with space and 中文.png",
		"quote\"inside",
		"back\\slash",
		"tab\there",
		"newline\nthere",
		"ctrl\x01char",
		"<script>&",
		"line\u2028sep\u2029",
		"emoji 😀",
		"",
	}
	for _, s := range cases {
		// 与生产一致：关闭 HTML 转义（旧实现用的就是 SetEscapeHTML(false)）。
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(s); err != nil {
			t.Fatalf("encode %q: %v", s, err)
		}
		want := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
		got := appendJSONString(nil, s)
		if string(got) != string(want) {
			t.Fatalf("appendJSONString(%q) = %s，期望 %s", s, got, want)
		}
	}
}

// TestAppendJSONRow 校验单个对象的输出可被标准库解析且字段一致。
func TestAppendJSONRow(t *testing.T) {
	got := appendJSONRow(nil, "a\"b.png", "https://x/pic/a%22b.png")
	var out map[string]string
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("解析失败: %v / %s", err, got)
	}
	if out["id"] != "a\"b.png" || out["link"] != "https://x/pic/a%22b.png" {
		t.Fatalf("字段 = %+v", out)
	}
	if empty := appendJSONRow(nil, "", "/pic/x.png"); strings.Contains(string(empty), "\"id\"") {
		t.Fatalf("空 id 不应输出 id 字段: %s", empty)
	}
}
