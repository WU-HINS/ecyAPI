package server

import (
	"ecyapi/internal/store"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
)

var digitsRe = regexp.MustCompile(`^\d+$`)

// handleJSON 动态 JSON 接口：/json?pc 返回单条，/json?pc=3 返回多条。
// 兼容 ?table=xxx / ?type=xxx / ?category=xxx 与 ?num=?count=?limit=?n= 数量参数。
func (s *Server) handleJSON(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	category := ""
	limit := 1

	// 第一优先：table/type/category
	for _, key := range []string{"table", "type", "category"} {
		if v := q.Get(key); v != "" {
			category = v
			break
		}
	}

	// 数量别名
	for _, key := range []string{"num", "count", "limit", "n"} {
		if v := q.Get(key); digitsRe.MatchString(v) {
			if n, _ := strconv.Atoi(v); n > 0 {
				limit = n
			}
			break
		}
	}

	// 其余参数：分类名=数量
	if category == "" || limit == 1 {
		for key, vals := range q {
			if key == "table" || key == "type" || key == "category" ||
				key == "num" || key == "count" || key == "limit" || key == "n" {
				continue
			}
			if store.IsValidIdentifierName(key) {
				category = key
				if len(vals) > 0 && digitsRe.MatchString(vals[0]) {
					if n, _ := strconv.Atoi(vals[0]); n > 0 {
						limit = n
					}
				}
				break
			}
			if digitsRe.MatchString(key) {
				if n, _ := strconv.Atoi(key); n > 0 {
					limit = n
				}
			}
		}
	}

	if category == "" || !store.IsValidIdentifierName(category) {
		s.renderHelloWorld(w)
		return
	}

	if limit > s.cfg.MaxLinks {
		limit = s.cfg.MaxLinks
	}
	if limit < 1 {
		limit = 1
	}

	rows, err := s.st.Random(r.Context(), category, limit)
	if err != nil || len(rows) == 0 {
		s.renderHelloWorld(w)
		return
	}

	// ?w=NNN 压缩宽度：透传到返回的图片链接，宽度校验由 /pic 预览侧完成。
	suffix := ""
	if v := q.Get("w"); v != "" {
		suffix = "?w=" + url.QueryEscape(v)
	}

	type rowJSON struct {
		ID   string `json:"id,omitempty"`
		Link string `json:"link"`
	}
	dataRows := make([]rowJSON, len(rows))
	links := make([]string, 0, len(rows))
	for i, row := range rows {
		abs := s.absoluteURL(r, row.Link) + suffix
		dataRows[i] = rowJSON{ID: row.ID, Link: abs}
		links = append(links, abs)
	}

	payload := map[string]any{
		"code":     200,
		"category": category,
		"count":    len(rows),
	}
	if limit == 1 {
		payload["data"] = dataRows[0]
	} else {
		payload["data"] = dataRows
		payload["links"] = links
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(payload)
}

// renderHelloWorld 与原版一致：非法参数返回你好世界页。
func (s *Server) renderHelloWorld(w http.ResponseWriter) {
	s.render(w, http.StatusOK, "hello.html", nil)
}
