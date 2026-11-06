package config

// roots.go —— 附加图库列表的持久化（data/roots.json）。
//
// 优先级：roots.json 存在且非空 > ECY_EXTRA_ROOTS 环境变量。
// 后台增删附加库时写这个文件，重启后生效。
//
// 这里不用 struct tag，改用 map 编解码，避免源码里出现反引号。

import (
	"encoding/json"
	"log"
	"os"
)

// LoadRootsFile 读取 roots.json；不存在或解析失败返回 nil（不阻断启动）。
func LoadRootsFile(path string) []ExtraRoot {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		log.Printf("roots.json 解析失败（忽略）: %v", err)
		return nil
	}
	arr, _ := doc["roots"].([]any)
	out := make([]ExtraRoot, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if en, ok := m["enabled"].(bool); ok && !en {
			continue
		}
		name, _ := m["name"].(string)
		p, _ := m["path"].(string)
		if name == "" || p == "" {
			continue
		}
		out = append(out, ExtraRoot{Name: name, Path: p})
	}
	return out
}

// SaveRootsFile 原子写 roots.json。
func SaveRootsFile(path string, roots []ExtraRoot) error {
	arr := make([]any, 0, len(roots))
	for _, r := range roots {
		arr = append(arr, map[string]any{"name": r.Name, "path": r.Path, "enabled": true})
	}
	b, err := json.MarshalIndent(map[string]any{"roots": arr}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
