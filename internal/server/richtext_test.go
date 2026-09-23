package server

import (
	"html/template"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// 用户反馈的用例：<br> 应生效换行。
func TestRenderRichTextLineBreak(t *testing.T) {
	got := string(renderRichText("如果你喜欢，那就分享给喜欢二次元风格的博主们吧<br>\nQQ群：721262816"))
	want := "如果你喜欢，那就分享给喜欢二次元风格的博主们吧<br />\nQQ群：721262816"
	if got != want {
		t.Fatalf("br not preserved:\n got=%q\nwant=%q", got, want)
	}
}

// 各种换行写法都应生效。
func TestRenderRichTextBrVariants(t *testing.T) {
	for _, in := range []string{"a<br>b", "a<br/>b", "a<br />b", "a<BR>b", "a<Br/>b"} {
		got := string(renderRichText(in))
		if !strings.Contains(got, "<br") {
			t.Fatalf("%q should produce a br tag, got %q", in, got)
		}
	}
}

// 白名单标签保留，非白名单标签按纯文本转义。
func TestRenderRichTextWhitelist(t *testing.T) {
	got := string(renderRichText(`<b>粗</b> <em>斜</em> <script>alert(1)</script>`))
	if !strings.Contains(got, "<b>粗</b>") || !strings.Contains(got, "<em>斜</em>") {
		t.Fatalf("whitelisted tags dropped: %s", got)
	}
	if strings.Contains(got, "<script") {
		t.Fatalf("script tag must be escaped: %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Fatalf("script tag should show as plain text: %s", got)
	}
}

// XSS 载荷必须被转义，且渲染结果整体是安全的 template.HTML。
func TestRenderRichTextXSREscaped(t *testing.T) {
	cases := []string{
		`<img src=x onerror=alert(1)>`,
		`<b onclick="alert(1)">x</b>`,
		`<a href="javascript:alert(1)">x</a>`,
		`<a href="data:text/html;base64,PHN2Zz4=">x</a>`,
		`"><script>alert(1)</script>`,
		`<iframe src="//evil.com"></iframe>`,
	}
	for _, in := range cases {
		got := string(renderRichText(in))
		low := strings.ToLower(got)
		// 结果中不允许出现可执行的标签或事件属性；被转义成纯文本（&lt;...）是安全的。
		for _, bad := range []string{"<img", "<script", "<iframe", "javascript:", "data:text/html"} {
			if strings.Contains(low, bad) {
				t.Fatalf("input %q leaked %q in output %q", in, bad, got)
			}
		}
		// 结果中不允许出现危险标签；事件属性仅当出现在真实标签内才算漏洞，
		// 被转义成纯文本（&lt;img ... onerror=...&gt;）不构成注入。
		for _, m := range regexp.MustCompile(`(?s)<[^>]*>`).FindAllString(low, -1) {
			if strings.Contains(m, "onclick") || strings.Contains(m, "onerror") || strings.Contains(m, "javascript:") {
				t.Fatalf("input %q kept dangerous attribute %q in %q", in, m, got)
			}
		}
	}
}

// a 标签：href 保留并转义，target="_blank" 自动补 rel。
func TestRenderRichTextLink(t *testing.T) {
	got := string(renderRichText(`<a href="https://example.com/a?b=1&amp;c=2" target="_blank">点我</a>`))
	if !strings.Contains(got, `href="https://example.com/a?b=1&amp;c=2"`) {
		t.Fatalf("href lost or double-escaped: %s", got)
	}
	if !strings.Contains(got, `rel="noopener noreferrer"`) {
		t.Fatalf("missing rel on _blank link: %s", got)
	}
	// 非白名单属性应被丢弃
	got = string(renderRichText(`<a href="https://example.com" onclick="alert(1)">x</a>`))
	if strings.Contains(got, "onclick") {
		t.Fatalf("non-whitelisted attribute kept: %s", got)
	}
}

// 不完整/畸形标签按纯文本处理，不产生残留标签。
func TestRenderRichTextMalformed(t *testing.T) {
	cases := []string{"a < b", "a <b", "<>", "<br", "3 < 5 > 2", "< b >"}
	for _, in := range cases {
		got := string(renderRichText(in))
		if strings.Contains(strings.ToLower(got), "<br") || strings.Contains(got, "<b>") {
			t.Fatalf("malformed input %q produced a tag: %q", in, got)
		}
	}
}

// 特殊字符应被转义，防止破坏页面结构。
func TestRenderRichTextEscapesAmpersand(t *testing.T) {
	got := string(renderRichText(`A & B "C" 'D'`))
	if !strings.Contains(got, "&amp;") || !strings.Contains(got, "&#34;") && !strings.Contains(got, "&quot;") {
		t.Fatalf("characters not escaped: %s", got)
	}
}

// 模板执行层验证：home 模板经 RichText 输出后，<br> 不再被转义。
func TestHomeShareRendersBr(t *testing.T) {
	s, cookie := newContentTestServer(t)
	postContent(t, s, cookie, url.Values{
		"share": {"如果你喜欢，那就分享给喜欢二次元风格的博主们吧<br>\nQQ群：721262816"},
	})
	home := getHome(t, s)
	if !strings.Contains(home, "QQ群：721262816") {
		t.Fatalf("share text missing: %s", home)
	}
	if strings.Contains(home, "&lt;br") {
		t.Fatalf("br still escaped in rendered home: %s", home)
	}
	if !strings.Contains(home, "<br />") && !strings.Contains(home, "<br>") {
		t.Fatalf("br not rendered as a tag in home: %s", home)
	}
}

// 首页文案里的脚本标签仍须被转义（后台内容不可信）。
func TestHomeShareScriptEscaped(t *testing.T) {
	s, cookie := newContentTestServer(t)
	postContent(t, s, cookie, url.Values{
		"share": {"<script>alert(1)</script>安全"},
	})
	home := getHome(t, s)
	if strings.Contains(home, "<script>alert(1)</script>") {
		t.Fatalf("script injected through share: %s", home)
	}
	if !strings.Contains(home, "安全") {
		t.Fatalf("plain text lost: %s", home)
	}
}

// 确保 renderRichText 的返回值类型可用于模板（template.HTML）。
var _ template.HTML = renderRichText("")
