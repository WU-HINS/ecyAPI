package server

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"ecyapi/internal/store"
)

var digitsRe = regexp.MustCompile(`^\d+$`)

// handleJSON 动态 JSON 接口：/json?pc 返回单条，/json?pc=3 返回多条。
// 兼容 ?table=xxx / ?type=xxx / ?category=xxx 与 ?num=?count=?limit=?n= 数量参数。
// ?orientation=landscape|portrait|auto 按方向筛选（auto 跟随 UA）。
func (s *Server) handleJSON(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	category := ""
	limit := 1
	want := store.OrientationAny

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
				key == "num" || key == "count" || key == "limit" || key == "n" ||
				key == "w" || key == "quantity" ||
				key == "orientation" || key == "orient" || key == "o" {
				continue
			}
			if s.validCategory(key) {
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

	if category == "" || !s.validCategory(category) {
		s.renderHelloWorld(w)
		return
	}

	if limit > s.cfg.MaxLinks {
		limit = s.cfg.MaxLinks
	}
	if limit < 1 {
		limit = 1
	}

	// 方向参数：显式值优先，auto 时跟随 UA（移动端竖图、桌面端横图）。
	for _, key := range []string{"orientation", "orient", "o"} {
		v := q.Get(key)
		if v == "" {
			continue
		}
		if o, ok := resolveOrientation(v, r); ok {
			want = o
		}
		break
	}

	rows, err := s.randomRows(r, category, limit, want)
	if err != nil || len(rows) == 0 {
		s.renderHelloWorld(w)
		return
	}

	// ?w=NNN 压缩宽度：透传到返回的图片链接，宽度校验由 /pic 预览侧完成。
	suffix := ""
	if v := q.Get("w"); v != "" {
		suffix = "?w=" + url.QueryEscape(v)
	}

	base := s.baseFor(r)
	buf := make([]byte, 0, 128+len(rows)*96)
	buf = append(buf, `{"code":200,"category":`...)
	buf = appendJSONString(buf, category)
	buf = append(buf, `,"count":`...)
	buf = appendJSONInt(buf, len(rows))
	if limit == 1 {
		buf = append(buf, `,"data":`...)
		buf = appendJSONRow(buf, rows[0].ID, joinURL(base, rows[0].Link, suffix))
	} else {
		buf = append(buf, `,"data":[`...)
		for i, row := range rows {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = appendJSONRow(buf, row.ID, joinURL(base, row.Link, suffix))
		}
		buf = append(buf, `],"links":[`...)
		for i, row := range rows {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = appendJSONString(buf, joinURL(base, row.Link, suffix))
		}
		buf = append(buf, ']')
	}
	buf = append(buf, '}', '\n')

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf)
}

// renderHelloWorld 与原版一致：非法参数返回你好世界页。
func (s *Server) renderHelloWorld(w http.ResponseWriter) {
	s.render(w, http.StatusOK, "hello.html", nil)
}
