package server

import (
	"regexp"
	"strings"
	"testing"
)

// TestRichTextSafeCorpus 用一批常见 XSS 载荷验证富文本渲染不会产出可执行标签。
func TestRichTextSafeCorpus(t *testing.T) {
	dangerousTags := []string{"<script", "<img", "<svg", "<iframe", "<object", "<embed",
		"<style", "<link", "<meta", "<form", "<input", "<body", "<html"}
	eventedTag := regexp.MustCompile(`(?is)<[a-z/][^>]*>`)
	payloads := []string{
		`<a href="javascript:alert(1)">x</a>`,
		`<a href=javascript:alert(1)>x</a>`,
		`<a href="JaVaScRiPt:alert(1)">x</a>`,
		`<a href=" data:text/html,x">y</a>`,
		`<b><script>alert(1)</script></b>`,
		`<span onclick="alert(1)">x</span>`,
		`<p onmouseover=alert(1)>x</p>`,
		`<a href="https://ok.com" onclick="alert(1)">x</a>`,
		`</b><script>alert(1)</script>`,
		`<br onload=alert(1)>`,
		`<a href="https://x.com" target="_blank" rel="x">y</a>`,
		`"><svg/onload=alert(1)>`,
	}
	for _, in := range payloads {
		out := safeRichText(in)
		low := strings.ToLower(out)
		for _, tag := range dangerousTags {
			if strings.Contains(low, tag) {
				t.Fatalf("payload %q leaked tag %q -> %q", in, tag, out)
			}
		}
		// 真实标签中不得残留事件处理器或危险协议
		for _, m := range eventedTag.FindAllString(low, -1) {
			for _, bad := range []string{"onclick", "onerror", "onload", "onmouseover", "javascript:", "vbscript:", "data:"} {
				if strings.Contains(m, bad) {
					t.Fatalf("payload %q leaked %q in tag %q", in, bad, m)
				}
			}
		}
	}
}
