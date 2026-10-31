package server

// order.go — 首页卡片（栏目 + 虚拟分组）的统一排序。
//
// 栏目顺序原本存在 endpoints.json 的数组顺序里，虚拟分组顺序存在 groups.json 里。
// 但用户在「栏目与排序」里是把两者放一起拖的，所以额外存一份统一顺序：
//   data/order.json  {"items":["/pc","@img/mix","/phone"]}
// 渲染首页时按这份顺序排列，缺失的条目按原顺序追加在末尾。

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
)

const homeOrderFile = "data/order.json"

type homeOrderDoc struct {
	Items []string `json:"items"`
}

// canonToken 把任意写法的顺序标记归一：分组 → "@img/<name>"，栏目 → "/<path>"。
// 表单里栏目是不带前导斜杠的（ep1），分组是 "@img/mix"，统一存归一后的形式。
func canonToken(tok string) string {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return ""
	}
	p := "/" + strings.TrimPrefix(tok, "/")
	if strings.HasPrefix(p, "/@img/") {
		return strings.TrimPrefix(p, "/")
	}
	return p
}

// cardToken 卡片在统一顺序里的标识：栏目用 /路径，虚拟分组用 @img/名字。
// 分组卡片的 Path 是 "/@img/<name>"（带前导斜杠），这里统一去掉再判断。
func cardToken(c homeCard) string {
	return canonToken(c.Path)
}

// loadHomeOrder 读取统一顺序（文件不存在就当作没设置）。
func (s *Server) loadHomeOrder() error {
	b, err := os.ReadFile(homeOrderFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var doc homeOrderDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return err
	}
	s.mu.Lock()
	s.homeOrder = doc.Items
	s.mu.Unlock()
	return nil
}

// saveHomeOrder 原子落盘并更新内存。
func (s *Server) saveHomeOrder(items []string) error {
	b, err := json.MarshalIndent(homeOrderDoc{Items: items}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll("data", 0o755); err != nil {
		return err
	}
	tmp := homeOrderFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, homeOrderFile); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	s.mu.Lock()
	s.homeOrder = append([]string(nil), items...)
	s.mu.Unlock()
	return nil
}

// orderCards 按统一顺序排卡片；未出现在顺序表里的保持原有相对顺序排在最后。
func (s *Server) orderCards(cards []homeCard) []homeCard {
	s.mu.RLock()
	order := append([]string(nil), s.homeOrder...)
	s.mu.RUnlock()
	if len(order) == 0 || len(cards) == 0 {
		return cards
	}
	rank := make(map[string]int, len(order))
	for i, tok := range order {
		if _, dup := rank[tok]; !dup {
			rank[tok] = i
		}
	}
	out := append([]homeCard(nil), cards...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, iok := rank[cardToken(out[i])]
		rj, jok := rank[cardToken(out[j])]
		switch {
		case iok && jok:
			return ri < rj
		case iok:
			return true
		case jok:
			return false
		}
		return false // 都没登记：保持原顺序
	})
	return out
}
