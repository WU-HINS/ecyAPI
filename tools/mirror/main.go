// mirror 把远端存储（WebDAV / S3）增量同步成本地目录，供 ecyAPI 直接扫描。
//
// 典型用法（OpenList 挂载的网盘当后端）：
//
//	mirror -url https://openlist.example.com/dav -user admin -pass xxx \
//	       -dest /srv/ecyapi/data/categories -workers 8 -prune
//
// 然后用 ECY_DATA_DIR 指向 -dest 启动服务即可；索引、缩略图、图库、内存控制全部复用。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ecyapi/internal/index"
	"ecyapi/internal/remote"
)

func main() {
	var (
		kind      = flag.String("src", "webdav", "数据源类型：webdav | s3")
		url       = flag.String("url", "", "WebDAV 地址（如 https://host/dav）或 S3 endpoint")
		user      = flag.String("user", "", "WebDAV 用户名 / S3 AccessKey")
		pass      = flag.String("pass", "", "WebDAV 密码 / S3 SecretKey")
		token     = flag.String("token", "", "WebDAV Bearer token（可选，优先于 user/pass）")
		bucket    = flag.String("bucket", "", "S3 桶名")
		prefix    = flag.String("prefix", "", "S3 前缀（相当于子目录）")
		region    = flag.String("region", "us-east-1", "S3 区域")
		pathStyle = flag.Bool("path-style", true, "S3 是否使用 path-style（MinIO/OpenList 常用）")
		dest      = flag.String("dest", "data/categories", "本地目标目录（= ECY_DATA_DIR）")
		workers   = flag.Int("workers", 4, "并发下载数")
		maxDepth  = flag.Int("max-depth", 3, "远端最大层级（与服务端 ECY_MAX_DEPTH 一致）")
		prune     = flag.Bool("prune", false, "删除本地多余文件（远端已删）")
		exts      = flag.String("ext", ".png,.jpg,.jpeg,.webp,.gif,.avif,.bmp,.jxl", "只同步这些后缀（* 表示全部；默认只图片）")
		dryRun    = flag.Bool("dry-run", false, "只打印计划，不落盘")
		interval  = flag.Duration("interval", 0, "常驻同步间隔（0 = 只跑一次，例如 5m）")
		mode      = flag.String("mode", "mirror", "mirror = 下载到本地；index = 只建云端索引；probe = 批量探测方向（只读 8KB 头）")
		indexPath = flag.String("index", "", "-mode=probe 时待探测的索引文件（如 index.web.bin）")
		probeMax  = flag.Int("probe-limit", 0, "最多探测多少条（0 = 全部未知）")
		probeWait = flag.Duration("probe-delay", 0, "每次探测后的休眠（限速，如 50ms）")
		out       = flag.String("out", "index.web.bin", "-mode=index 时的输出文件")
	)
	flag.Parse()

	if *url == "" {
		fmt.Fprintln(os.Stderr, "必须提供 -url")
		os.Exit(2)
	}
	var extsList []string
	if strings.TrimSpace(*exts) != "*" {
		for _, e := range strings.Split(*exts, ",") {
			if e = strings.TrimSpace(e); e != "" {
				extsList = append(extsList, e)
			}
		}
	}

	var src remote.Source
	switch strings.ToLower(*kind) {
	case "webdav":
		w, err := remote.NewWebDAV(*url, *user, *pass, *token)
		if err != nil {
			log.Fatalf("初始化 WebDAV 失败: %v", err)
		}
		src = w
	case "s3":
		if *bucket == "" {
			log.Fatal("s3 模式必须提供 -bucket")
		}
		s, err := remote.NewS3(remote.S3Options{
			Endpoint: *url, Region: *region, Bucket: *bucket, Prefix: *prefix,
			AccessKey: *user, SecretKey: *pass, PathStyle: *pathStyle,
		})
		if err != nil {
			log.Fatalf("初始化 S3 失败: %v", err)
		}
		src = s
	default:
		log.Fatalf("未知数据源类型: %s（支持 webdav / s3）", *kind)
	}

	opt := remote.MirrorOptions{
		Dest: *dest, Workers: *workers, MaxDepth: *maxDepth,
		Prune: *prune, Extensions: extsList, DryRun: *dryRun, Logger: log.Default(),
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if strings.EqualFold(*mode, "probe") {
		if *indexPath == "" {
			log.Fatalf("-mode=probe 需要 -index <索引文件>")
		}
		ix, err := index.Open(*indexPath)
		if err != nil {
			log.Fatalf("打开索引失败: %v", err)
		}
		defer ix.Close()
		log.Printf("开始批量探测方向：%s（workers=%d，limit=%d）", src.Describe(), *workers, *probeMax)
		start := time.Now()
		res := remote.ProbeIndex(ctx, ix, src, remote.ProbeOptions{
			Workers: *workers, Limit: *probeMax, Delay: *probeWait, Logger: log.Default(),
		})
		log.Printf("探测完成：扫描 %d，未知 %d，成功 %d，失败 %d，共读 %.1f MB，耗时 %s",
			res.Scanned, res.Unknown, res.Probed, res.Failed,
			float64(res.ReadBytes)/1048576, time.Since(start).Round(time.Second))
		return
	}

	if strings.EqualFold(*mode, "index") {
		log.Printf("只建索引（不下载原图）：%s → %s", src.Describe(), *out)
		n, err := remote.BuildIndexFromSource(ctx, src, *out, remote.IndexOptions{
			MaxDepth: *maxDepth, Extensions: extsList, Logger: log.Default(),
		})
		if err != nil {
			log.Fatalf("建索引失败: %v", err)
		}
		log.Printf("完成：%d 条", n)
		return
	}

	run := func() {
		log.Printf("开始同步：%s → %s", src.Describe(), *dest)
		if _, err := remote.Mirror(ctx, src, opt); err != nil {
			log.Printf("同步失败: %v", err)
		}
	}
	run()
	if *interval <= 0 {
		return
	}
	t := time.NewTicker(*interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Print("收到退出信号，停止常驻同步")
			return
		case <-t.C:
			run()
		}
	}
}
