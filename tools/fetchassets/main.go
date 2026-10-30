// Command fetchassets 把看板娘的静态资源下载到本地目录。
//
//	go run ./tools/fetchassets                  # 下载到 data/static
//	go run ./tools/fetchassets -dir /srv/static # 指定目录
//
// 服务端也内置了同样能力：ecyapi -fetch-assets（Docker 里补素材最方便）。
package main

import (
	"flag"
	"log"
	"os"

	"ecyapi/internal/petassets"
)

func main() {
	dir := flag.String("dir", "data/static", "下载目标目录")
	base := flag.String("base", petassets.DefaultBase, "资源根地址")
	flag.Parse()

	logger := log.New(os.Stderr, "", log.LstdFlags)
	if _, err := petassets.Fetch(*dir, *base, logger.Printf); err != nil {
		logger.Fatalf("%v", err)
	}
}
