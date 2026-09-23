package server

import (
	"html"
	"html/template"
	"strings"
)

// 首页文案（标语 / 分享语 / 备案号文字）支持一小部分常用 HTML 标签，
// 让后台填写的文案可以换行、加粗、放链接。
//
// 实现方式：先按标签白名单逐个扫描，只保留白名单内的标签，
// 其余内容一律当作纯文本转义输出。也就是说除白名单标签外，
// 所有 < > & " 都会被转义，不存在 XSS 注入面。
// 支持 <br>、<br/>、<br /> 多种写法；a 标签仅保留 href/title/target/rel，
// 并拦截 javascript:/data: 等危险协议。

// richTag 白名单标签：attributes 为该标签允许保留的属性（小写）。
type richTag struct {
	attributes map[string]bool
	void       bool // 空元素（无闭合标签），如 br
}

// richTags 标签白名单，键为小写标签名。
var richTags = map[string]richTag{
	"br":     {void: true},
	"b":      {},
	"strong": {},
	"i":      {},
	"em":     {},
	"u":      {},
	"s":      {},
	"del":    {},
	"small":  {},
	"sub":    {},
	"sup":    {},
	"span":   {},
	"p":      {},
	"code":   {},
	"mark":   {},
	"a":      {attributes: map[string]bool{"href": true, "title": true, "target": true, "rel": true}},
}

// renderRichText 将文案转为可安全嵌入页面的 HTML：
// 仅白名单标签原样保留，其余内容全部转义，避免 XSS。
func renderRichText(s string) template.HTML {
	return template.HTML(safeRichText(s)) // #nosec G203 -- 内容已按白名单过滤并转义
}

// safeRichText 按标签白名单过滤文案，返回可安全嵌入页面的 HTML 字符串。
func safeRichText(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)
	for i := 0; i < len(s); {
		if s[i] != '<' {
			j := strings.IndexByte(s[i:], '<')
			if j < 0 {
				b.WriteString(escapeRichText(s[i:]))
				break
			}
			b.WriteString(escapeRichText(s[i : i+j]))
			i += j
			continue
		}
		tag, next, ok := parseRichTag(s, i)
		if !ok {
			// 非白名单标签或畸形标签，当作纯文本转义。
			b.WriteString("&lt;")
			i++
			continue
		}
		b.WriteString(tag)
		i = next
	}
	return b.String()
}

// parseRichTag 尝试从 s[from]（s[from] 必为 '<'）解析一个白名单标签。
// 返回还原后的标签文本、标签结束后的下标，以及是否解析成功。
func parseRichTag(s string, from int) (string, int, bool) {
	i := from + 1
	if i >= len(s) {
		return "", 0, false
	}
	closing := false
	if s[i] == '/' {
		closing = true
		i++
	}
	// 读取标签名
	start := i
	for i < len(s) && isTagNameByte(s[i]) {
		i++
	}
	name := strings.ToLower(s[start:i])
	if name == "" {
		return "", 0, false
	}
	spec, ok := richTags[name]
	if !ok {
		return "", 0, false
	}
	if closing && spec.void {
		return "", 0, false
	}
	// 标签名与后续内容之间必须紧邻 '>' 或空白（避免 <b2> 之类被误判）
	if i < len(s) && !isSpaceByte(s[i]) && s[i] != '>' && s[i] != '/' {
		return "", 0, false
	}
	// 定位标签结尾：引号内的 '>' 不算结尾，避免属性值提前截断标签。
	end := -1
	quote := byte(0)
	for k := i; k < len(s); k++ {
		c := s[k]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == '>' {
			end = k
			break
		}
	}
	if end < 0 {
		return "", 0, false
	}
	attrs := s[i:end]
	// 属性解析：非 a 标签直接丢弃全部属性；a 标签仅保留白名单属性。
	kept := ""
	if !closing {
		kept = filterRichAttrs(attrs, spec)
	}
	selfClose := strings.HasSuffix(strings.TrimSpace(attrs), "/")
	var b strings.Builder
	b.WriteByte('<')
	if closing {
		b.WriteByte('/')
	}
	b.WriteString(name)
	b.WriteString(kept)
	if !closing && (selfClose || spec.void) {
		b.WriteString(" /")
	}
	b.WriteByte('>')
	return b.String(), end + 1, true
}

// filterRichAttrs 按白名单过滤属性，属性值做转义并拒绝危险协议。
func filterRichAttrs(raw string, spec richTag) string {
	if len(spec.attributes) == 0 {
		return ""
	}
	var out strings.Builder
	for _, attr := range splitRichAttrs(raw) {
		name, value, hasValue := parseRichAttr(attr)
		if name == "" || !spec.attributes[name] {
			continue
		}
		if name == "target" && strings.EqualFold(strings.TrimSpace(value), "_blank") {
			value = "_blank"
		} else if name == "target" {
			continue
		}
		if name == "rel" && !strings.EqualFold(strings.TrimSpace(value), "noopener noreferrer") {
			value = "noopener noreferrer"
		}
		if name == "href" {
			v := strings.TrimSpace(value)
			lower := strings.ToLower(v)
			// 仅允许 http/https/mailto/相对路径与锚点，拦截 javascript: 等。
			if strings.HasPrefix(lower, "javascript:") || strings.HasPrefix(lower, "data:") ||
				strings.HasPrefix(lower, "vbscript:") {
				continue
			}
			value = v
		}
		out.WriteByte(' ')
		out.WriteString(name)
		if hasValue {
			out.WriteString(`="`)
			out.WriteString(escapeRichText(value))
			out.WriteByte('"')
		}
	}
	// 带 target="_blank" 的外链自动补 rel，防止反向标签劫持。
	if spec.attributes["target"] && strings.Contains(out.String(), `target="_blank"`) &&
		!strings.Contains(out.String(), "rel=") {
		out.WriteString(` rel="noopener noreferrer"`)
	}
	return out.String()
}

// splitRichAttrs 以空白切分属性片段（引号内的空白不切分）。
func splitRichAttrs(raw string) []string {
	var (
		parts []string
		cur   strings.Builder
		quote byte
	)
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
			cur.WriteByte(c)
		case c == '"' || c == '\'':
			quote = c
			cur.WriteByte(c)
		case isSpaceByte(c):
			if cur.Len() > 0 {
				parts = append(parts, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

// parseRichAttr 解析单个属性片段，返回小写属性名、去引号的值与是否存在值。
func parseRichAttr(raw string) (name, value string, hasValue bool) {
	eq := strings.IndexByte(raw, '=')
	if eq < 0 {
		return strings.ToLower(strings.TrimSpace(raw)), "", false
	}
	name = strings.ToLower(strings.TrimSpace(raw[:eq]))
	value = strings.TrimSpace(raw[eq+1:])
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		value = value[1 : len(value)-1]
	}
	return name, value, true
}

func isTagNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

// richEntities 后台文案里可能出现的 HTML 实体，先还原再统一转义，避免二次转义（&amp;amp;）。
var richEntities = strings.NewReplacer(
	"&amp;", "&",
	"&lt;", "<",
	"&gt;", ">",
	"&quot;", "\"",
	"&#34;", "\"",
	"&#39;", "'",
	"&apos;", "'",
	"&nbsp;", "\u00a0",
)

// escapeRichText 先还原常见 HTML 实体，再做一次转义，保证输出里只转义一次。
func escapeRichText(s string) string {
	return html.EscapeString(richEntities.Replace(s))
}
