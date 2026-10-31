// mkbench 生成压测数据集：在 -dir 下创建 -cats 个分类，每个分类 -files 张图。
//
//	go run ./tools/mkbench -dir bench/categories -cats 1000 -files 50
//
// 所有文件硬链接到同一张 64x64 PNG，既不占空间也保持「真实文件」的扫描/读取成本。
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

func main() {
	dir := flag.String("dir", "bench/categories", "数据集输出目录")
	cats := flag.Int("cats", 1000, "分类数量")
	files := flag.Int("files", 50, "每个分类的图片数")
	flag.Parse()

	src := filepath.Join(os.TempDir(), "mkbench-one.png")
	if err := writePNG(src); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for c := 0; c < *cats; c++ {
		d := filepath.Join(*dir, fmt.Sprintf("c%03d", c))
		if err := os.MkdirAll(d, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		for i := 0; i < *files; i++ {
			dst := filepath.Join(d, fmt.Sprintf("f%02d.png", i))
			_ = os.Remove(dst)
			if err := os.Link(src, dst); err != nil {
				if err := copyFile(src, dst); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(1)
				}
			}
		}
	}
	fmt.Printf("数据集就绪：%d 分类 × %d 张 = %d 张\n", *cats, *files, *cats**files)
}

func writePNG(path string) error {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 4), uint8(y * 4), 128, 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
