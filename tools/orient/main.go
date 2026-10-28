package main

// orient — 按图片真实宽高把文件分到横版 / 竖版目录（只读头，不解码像素）。
// 用法：
//   go run ./tools/orient -src data/categories/pc -land data/categories/pc_land
//   go run ./tools/orient -src data/categories/pc -land DIR -port DIR -apply
// 默认 dry-run，只打印计划；-apply 才真正 mv。
import (
	"flag"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"sort"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

type item struct {
	name string
	w, h int
}

func orientation(w, h int) string {
	switch {
	case w > h:
		return "landscape"
	case h > w:
		return "portrait"
	default:
		return "square"
	}
}

func main() {
	src := flag.String("src", "", "源目录（扁平存放图片）")
	land := flag.String("land", "", "横版目标目录（w > h）")
	port := flag.String("port", "", "竖版目标目录（h > w）")
	apply := flag.Bool("apply", false, "真正移动文件（默认只打印）")
	flag.Parse()
	if *src == "" || *land == "" || *port == "" {
		fmt.Fprintln(os.Stderr, "必须指定 -src -land -port")
		os.Exit(2)
	}
	entries, err := os.ReadDir(*src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读目录失败: %v\n", err)
		os.Exit(1)
	}
	var (
		items             []item
		skipped           []string
		langN, portN, sqN int
	)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		f, err := os.Open(filepath.Join(*src, e.Name()))
		if err != nil {
			skipped = append(skipped, e.Name()+" (open: "+err.Error()+")")
			continue
		}
		cfg, _, derr := image.DecodeConfig(f) // 只读头部，O(1) 内存
		f.Close()
		if derr != nil {
			skipped = append(skipped, e.Name()+" (decode: "+derr.Error()+")")
			continue
		}
		items = append(items, item{e.Name(), cfg.Width, cfg.Height})
		switch orientation(cfg.Width, cfg.Height) {
		case "landscape":
			langN++
		case "portrait":
			portN++
		default:
			sqN++
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })
	fmt.Printf("扫描 %d 张：横版 %d，竖版 %d，正方形 %d\n", len(items), langN, portN, sqN)
	for _, it := range items {
		dst := *land
		if orientation(it.w, it.h) == "portrait" {
			dst = *port
		}
		if orientation(it.w, it.h) == "square" {
			fmt.Printf("  %-40s %5dx%-5d 正方形，跳过\n", it.name, it.w, it.h)
			continue
		}
		if *apply {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				fmt.Fprintf(os.Stderr, "建目录失败 %s: %v\n", dst, err)
				continue
			}
			if err := os.Rename(filepath.Join(*src, it.name), filepath.Join(dst, it.name)); err != nil {
				fmt.Fprintf(os.Stderr, "移动失败 %s: %v\n", it.name, err)
				continue
			}
			fmt.Printf("  %-40s %5dx%-5d -> %s\n", it.name, it.w, it.h, dst)
			continue
		}
		fmt.Printf("  %-40s %5dx%-5d -> %s (dry-run)\n", it.name, it.w, it.h, dst)
	}
	for _, s := range skipped {
		fmt.Printf("  跳过 %s\n", s)
	}
	if !*apply {
		fmt.Println("以上为 dry-run，加 -apply 执行移动")
	}
}
