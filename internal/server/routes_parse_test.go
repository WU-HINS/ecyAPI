package server

import "testing"

// 旧版纯数组格式 [...] 应能正常解析（线上事故：旧格式被判定为不合法）。
func TestParseEndpointsFileLegacyArray(t *testing.T) {
	data := []byte(`[{"path":"cat","desk":"cat","mobile":"","label":"CAT","desc":"","kind":"image"}]`)
	items, order, err := parseEndpointsFile(data)
	if err != nil {
		t.Fatalf("legacy array should parse, got error: %v", err)
	}
	if len(items) != 1 || items[0].Path != "cat" {
		t.Fatalf("unexpected items: %+v", items)
	}
	if order != nil {
		t.Fatalf("legacy array should have no order, got %v", order)
	}
}

// 新版对象格式 {"order":[...],"items":[...]} 正常解析。
func TestParseEndpointsFileObject(t *testing.T) {
	data := []byte(`{"order":["b","a"],"items":[{"path":"a","desk":"a","kind":"image"},{"path":"b","desk":"b","kind":"image"}]}`)
	items, order, err := parseEndpointsFile(data)
	if err != nil {
		t.Fatalf("object format should parse, got error: %v", err)
	}
	if len(items) != 2 || len(order) != 2 || order[0] != "b" {
		t.Fatalf("unexpected result: items=%+v order=%v", items, order)
	}
}

// 新版对象格式 items 为空数组（栏目被删光）时应视为合法空列表，而非回退到旧格式解析。
func TestParseEndpointsFileEmptyItems(t *testing.T) {
	data := []byte(`{"order":[],"items":[]}`)
	items, order, err := parseEndpointsFile(data)
	if err != nil {
		t.Fatalf("empty items object should parse, got error: %v", err)
	}
	if len(items) != 0 || len(order) != 0 {
		t.Fatalf("unexpected result: items=%+v order=%v", items, order)
	}
}

// 完全非法格式应报错（不误伤）。
func TestParseEndpointsFileInvalid(t *testing.T) {
	if _, _, err := parseEndpointsFile([]byte(`{"foo":1}`)); err != nil {
		t.Fatalf("object without items should be tolerated, got: %v", err)
	}
	if _, _, err := parseEndpointsFile([]byte(`not json`)); err == nil {
		t.Fatal("garbage should error")
	}
}

// 损坏/异常对象（空对象、仅 order、items 为 null、非法类型）不再报
// missing required field "items"，而是视为空栏目列表，保证启动不被阻断。
func TestParseEndpointsFileToleratesBrokenObjects(t *testing.T) {
	cases := []string{
		`{}`,
		`{"order":["a"]}`,
		`{"order":null,"items":null}`,
	}
	for _, data := range cases {
		items, order, err := parseEndpointsFile([]byte(data))
		if err != nil {
			t.Fatalf("parse %s: unexpected error: %v", data, err)
		}
		if len(items) != 0 || len(order) != 0 {
			t.Fatalf("parse %s: expect empty result, got items=%+v order=%v", data, items, order)
		}
	}
}
