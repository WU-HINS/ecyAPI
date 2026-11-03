package index

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func buildN(t *testing.T, n int) (string, *Index) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "index.bin")
	b := &Builder{}
	perCat := 500
	cats := n / perCat
	if cats == 0 {
		cats = 1
	}
	left := n
	for c := 0; c < cats; c++ {
		items := perCat
		if left < items {
			items = left
		}
		left -= items
		cb := CatBuild{Name: fmt.Sprintf("cat%04d", c), Flags: 0}
		for i := 0; i < items; i++ {
			cb.Items = append(cb.Items, Entry{
				Name:  fmt.Sprintf("photo-%05d.jpg", i),
				Size:  int64(100000 + i),
				MTime: int64(1700000000 + i),
				Flags: OriLand,
			})
		}
		b.AddCategory(cb)
	}
	start := time.Now()
	if err := b.Build(path); err != nil {
		t.Fatal(err)
	}
	t.Logf("构建 %d 条耗时 %v", n, time.Since(start).Round(time.Millisecond))
	ix, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, ix
}

// TestIndexBasics 基本读写与查找。
func TestIndexBasics(t *testing.T) {
	path, ix := buildN(t, 2000)
	defer ix.Close()
	fi, _ := os.Stat(path)
	t.Logf("2000 条文件大小 %.1f KB", float64(fi.Size())/1024)

	cats := ix.Categories()
	if len(cats) != 4 {
		t.Fatalf("分类数 = %d", len(cats))
	}
	c, ok := ix.Find("cat0000")
	if !ok || c.count != 500 {
		t.Fatalf("cat0000 条目 = %d", c.count)
	}
	rec, ok := ix.Lookup(c, "photo-00042.jpg")
	if !ok || rec.Size != 100042 || rec.Flags != OriLand {
		t.Fatalf("查找结果不对: %+v ok=%v", rec, ok)
	}
	if _, ok := ix.Lookup(c, "nope.jpg"); ok {
		t.Fatal("不该找到")
	}
	// 回写方向位
	if err := ix.SetFlags(c, 0, OriPort); err != nil {
		t.Fatalf("SetFlags: %v", err)
	}
	if r, _ := ix.At(c, 0); r.Flags != OriPort {
		t.Fatalf("回写失败: %+v", r)
	}
}

// TestIndexMillion 100 万条：落盘大小、常驻内存、查询延迟。
func TestIndexMillion(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	const n = 1_000_000
	path, ix := buildN(t, n)
	defer ix.Close()

	fi, _ := os.Stat(path)
	mb := float64(fi.Size()) / 1048576
	t.Logf("100 万条：文件 %.1f MB（%.0f 字节/条，含路径）", mb, float64(fi.Size())/float64(n))
	// 目标：≤60MB（28B 记录 + 平均 ~17B 路径；文件大小不是 RSS，mmap 按页调入）
	if mb > 60 {
		t.Fatalf("文件过大：%.1f MB（目标 ≤60MB）", mb)
	}

	var ms runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms)
	t.Logf("打开后堆占用 %.1f MB（mmap 不计入堆）", float64(ms.HeapAlloc)/1048576)

	// 随机查询延迟
	cats := ix.Categories()
	if len(cats) == 0 {
		t.Fatal("没有分类")
	}
	const q = 200000
	start := time.Now()
	hit := 0
	for i := 0; i < q; i++ {
		c := cats[i%len(cats)]
		name := fmt.Sprintf("photo-%05d.jpg", (i*7919)%c.count)
		if _, ok := ix.Lookup(c, name); ok {
			hit++
		}
	}
	el := time.Since(start)
	t.Logf("%d 次随机查询：%v（%.1f µs/次），命中 %d", q, el.Round(time.Millisecond), float64(el.Microseconds())/q, hit)
	if el > 10*time.Second {
		t.Fatalf("查询太慢：%v", el)
	}
}

// BenchmarkLookup1M 单次查找基准（1 万条分类、每类 500）。
func BenchmarkLookup1M(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "index.bin")
	bl := &Builder{}
	for c := 0; c < 2000; c++ {
		cb := CatBuild{Name: fmt.Sprintf("cat%04d", c)}
		for i := 0; i < 500; i++ {
			cb.Items = append(cb.Items, Entry{Name: fmt.Sprintf("photo-%05d.jpg", i), Size: 1000, MTime: 1700000000})
		}
		bl.AddCategory(cb)
	}
	if err := bl.Build(path); err != nil {
		b.Fatal(err)
	}
	ix, err := Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer ix.Close()
	cats := ix.Categories()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c := cats[i%len(cats)]
			_, _ = ix.Lookup(c, fmt.Sprintf("photo-%05d.jpg", i%500))
			i++
		}
	})
}
