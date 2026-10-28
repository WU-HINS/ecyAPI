package server

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// 本文件提供 JSON 热路径的快速序列化：手写拼接，不构造 map[string]any、
// 不走反射编码，避免每请求数十次分配。
// 任何含非 ASCII / 控制字符 / 引号 / 反斜杠的字符串都回退到 encoding/json，
// 转义规则与标准库保持一致（含 U+2028 等边界）。

// appendJSONString 追加一个 JSON 字符串字面量。
func appendJSONString(dst []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			return appendJSONStringSlow(dst, s)
		}
	}
	dst = append(dst, '"')
	dst = append(dst, s...)
	return append(dst, '"')
}

// appendJSONStringSlow 慢路径：用标准库编码，并保持与旧实现一致的
// SetEscapeHTML(false)（默认的 json.Marshal 会把 < > & 转成 \u003c 等）。
func appendJSONStringSlow(dst []byte, s string) []byte {
	var buf bytes.Buffer
	buf.Grow(len(s) + 8)
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return append(dst, '"', '"')
	}
	b := buf.Bytes()
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	return append(dst, b...)
}

// appendJSONInt 追加整数。
func appendJSONInt(dst []byte, n int) []byte {
	return strconv.AppendInt(dst, int64(n), 10)
}

// appendJSONRow 追加 {"id":...,"link":...}；id 为空时只写 link。
func appendJSONRow(dst []byte, id, link string) []byte {
	dst = append(dst, '{')
	if id != "" {
		dst = append(dst, `"id":`...)
		dst = appendJSONString(dst, id)
		dst = append(dst, ',')
	}
	dst = append(dst, `"link":`...)
	dst = appendJSONString(dst, link)
	return append(dst, '}')
}
