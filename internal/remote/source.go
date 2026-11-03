// Package remote 把「远端存储」抽象成 ECY 能消费的本地镜像。
//
// 设计取舍（详见 docs/remote-backends.md）：
//   - 不做「远程直接索引」：现有索引/缩略图/图库缓存全都建立在本地区之上，
//     直接改会造成大量并发与缓存语义问题；
//   - 改为「远端 → 本地镜像」：mirror 把 WebDAV / S3 的目录树增量同步到本地，
//     服务端照常扫描本地目录，索引、预览、图库、内存控制全部复用，风险最小。
//
// 这样 OpenList（AList 系）挂的各大网盘，只要它暴露 WebDAV，就能当稳定后端。
package remote

import (
	"context"
	"io"
	"time"
)

// Entry 远端目录里的一项。
type Entry struct {
	Path    string    // 相对根的路径，用 / 分隔，不带前导斜杠
	Size    int64     // 文件字节数；目录为 0
	ModTime time.Time // 修改时间；远端不提供时为 0
	IsDir   bool
	ETag    string // 可选，用于快速判重
}

// Source 只读的远端数据源。
type Source interface {
	// List 列出 dir（相对根，"" 表示根）下的直接子项。
	List(ctx context.Context, dir string) ([]Entry, error)
	// Open 打开一个文件的读取流。
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	// Describe 返回数据源标识（日志用）。
	Describe() string
}
