// Package store 提供随机图片的数据访问：以本地目录为唯一数据源。
//
// 数据目录结构：data/categories/<分类名>/<图片文件>
// 图片不再以外部链接存储，而是图片文件本身存放在项目目录中。
package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime/multipart"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNotFound 表示分类不存在或分类内无有效图片。
var ErrNotFound = errors.New("not found")

// imageExts 允许作为图片/动图资源访问的扩展名。
var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
	".webp": true, ".bmp": true, ".avif": true, ".svg": true,
	".mp4": true, ".webm": true,
}

var identifierRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

// IsValidIdentifier 校验分类名，防路径穿越。
func IsValidIdentifier(name string) bool { return identifierRe.MatchString(name) }

// IsValidIdentifierName 供其它包复用的分类名校验。
func IsValidIdentifierName(name string) bool { return identifierRe.MatchString(name) }

// Row 一条图片记录。Link 为站内可访问的图片地址。
type Row struct {
	ID   string // 文件名
	Link string // 站内访问地址，如 /pic/pc/xxx.webp
}

// Store 统一数据访问接口。
type Store interface {
	Categories(ctx context.Context) ([]string, error)
	Count(ctx context.Context, category string) (int, error)
	Random(ctx context.Context, category string, limit int) ([]Row, error)
	ByIndex(ctx context.Context, category string, index int) (Row, error)
	Links(ctx context.Context, category string) ([]Row, error)
}

// Admin 后台管理写操作接口。
type Admin interface {
	Store
	// CreateCategory 新建分类目录。
	CreateCategory(ctx context.Context, name string) error
	// SaveFile 保存上传文件到分类，返回保存后的文件名。
	SaveFile(ctx context.Context, category, filename string, r io.Reader) (string, error)
	// DeleteFiles 批量删除分类内文件。
	DeleteFiles(ctx context.Context, category string, names []string) (int, error)
	// DeleteCategory 删除整个分类目录及其全部图片。
	DeleteCategory(ctx context.Context, name string) error
}

// ---------------------------------------------------------------- 文件实现

// reloadInterval 磁盘目录重扫的节流间隔。
// 读请求最多每 3 秒触发一次磁盘扫描；写操作（上传/删除/新建分类）会立即失效缓存。
const reloadInterval = 3 * time.Second

type fileStore struct {
	dir string

	// 惰性重载节流：lastScan 用 atomic 读，热路径无锁。
	reloadMu sync.Mutex
	lastScan atomic.Int64 // UnixNano

	mu    sync.RWMutex
	cache map[string][]string // category -> file names
}

// NewFileStore 使用 data/categories/<分类名>/ 子目录存放图片文件。
// 兼容旧版 <分类名>.txt 链接文件：迁移为原始链接仍可访问（若存在）。
func NewFileStore(dir string) (Store, error) {
	fs := &fileStore{dir: dir, cache: map[string][]string{}}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir %s: %w", dir, err)
	}
	if err := fs.scan(); err != nil {
		return nil, err
	}
	fs.lastScan.Store(time.Now().UnixNano())
	return fs, nil
}

func isImageFile(name string) bool {
	return imageExts[strings.ToLower(filepath.Ext(name))]
}

// scan 同步扫描磁盘目录并整体替换缓存。
// 分类目录存在但为空时会保留空列表，使 Count 返回 0 而非 ErrNotFound。
func (f *fileStore) scan() error {
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return fmt.Errorf("read data dir %s: %w", f.dir, err)
	}
	next := make(map[string][]string, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !IsValidIdentifier(name) {
			continue
		}
		sub, err := os.ReadDir(filepath.Join(f.dir, name))
		if err != nil {
			continue
		}
		var files []string
		for _, se := range sub {
			if se.IsDir() || !isImageFile(se.Name()) {
				continue
			}
			files = append(files, se.Name())
		}
		sort.Strings(files)
		next[name] = files
	}
	f.mu.Lock()
	f.cache = next
	f.mu.Unlock()
	return nil
}

// refreshIfStale 节流惰性重载：最多每 reloadInterval 触发一次磁盘扫描。
// 并发下用双重检查保证同时只有一个 goroutine 扫描；扫描持锁时间仅数毫秒，
// 读路径绝大多数时候只比较一次时间戳，零阻塞。
func (f *fileStore) refreshIfStale() {
	if time.Since(time.Unix(0, f.lastScan.Load())) < reloadInterval {
		return
	}
	f.reloadMu.Lock()
	defer f.reloadMu.Unlock()
	if time.Since(time.Unix(0, f.lastScan.Load())) < reloadInterval {
		return
	}
	_ = f.scan()
	f.lastScan.Store(time.Now().UnixNano())
}

// reload 同步强制重载（写路径使用）。
func (f *fileStore) reload() error {
	f.reloadMu.Lock()
	defer f.reloadMu.Unlock()
	err := f.scan()
	f.lastScan.Store(time.Now().UnixNano())
	return err
}

func (f *fileStore) categoryFiles(category string) ([]string, bool) {
	f.mu.RLock()
	files, ok := f.cache[category]
	f.mu.RUnlock()
	return files, ok
}

func (f *fileStore) Categories(ctx context.Context) ([]string, error) {
	_ = ctx
	f.refreshIfStale() // 节流重载，热更新目录
	f.mu.RLock()
	defer f.mu.RUnlock()
	names := make([]string, 0, len(f.cache))
	for name := range f.cache {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func (f *fileStore) Count(ctx context.Context, category string) (int, error) {
	_ = ctx
	files, ok := f.categoryFiles(category)
	if !ok {
		return 0, ErrNotFound
	}
	return len(files), nil
}

// Random 随机抽取 limit 个文件名（不放回）。
// 使用 rand/v2 全局 ChaCha8 随机源，自动按 goroutine 分片，无锁竞争；
// 且仅对部分元素做 Fisher-Yates 洗牌，开销 O(limit) 而非 O(n)。
func (f *fileStore) Random(ctx context.Context, category string, limit int) ([]Row, error) {
	_ = ctx
	files, ok := f.categoryFiles(category)
	if !ok {
		return nil, ErrNotFound
	}
	n := len(files)
	if n == 0 {
		return nil, ErrNotFound
	}
	if limit > n {
		limit = n
	}
	// 对 n-limit+1 .. n-1 做部分洗牌，选中前 limit 个。
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	for i := 0; i < limit; i++ {
		j := i + rand.IntN(n-i)
		idx[i], idx[j] = idx[j], idx[i]
	}
	rows := make([]Row, 0, limit)
	for _, i := range idx[:limit] {
		rows = append(rows, Row{ID: files[i], Link: "/pic/" + category + "/" + files[i]})
	}
	return rows, nil
}

func (f *fileStore) ByIndex(ctx context.Context, category string, index int) (Row, error) {
	_ = ctx
	if index < 1 {
		return Row{}, ErrNotFound
	}
	files, ok := f.categoryFiles(category)
	if !ok || index > len(files) {
		return Row{}, ErrNotFound
	}
	name := files[index-1]
	return Row{ID: name, Link: "/pic/" + category + "/" + name}, nil
}

func (f *fileStore) Links(ctx context.Context, category string) ([]Row, error) {
	_ = ctx
	files, ok := f.categoryFiles(category)
	if !ok {
		return nil, ErrNotFound
	}
	rows := make([]Row, 0, len(files))
	for _, name := range files {
		rows = append(rows, Row{ID: name, Link: "/pic/" + category + "/" + name})
	}
	return rows, nil
}

// NewFileAdmin 包装文件存储以支持后台写操作。
func NewFileAdmin(s Store) (Admin, error) {
	fs, ok := s.(*fileStore)
	if !ok {
		return nil, errors.New("store is not file backed")
	}
	return fs, nil
}

func (f *fileStore) CreateCategory(ctx context.Context, name string) error {
	_ = ctx
	if !IsValidIdentifier(name) {
		return errors.New("invalid category name")
	}
	path := filepath.Join(f.dir, name)
	if _, err := os.Stat(path); err == nil {
		return errors.New("分类已存在")
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	return f.reload()
}

var unsafeCharsRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// sanitizeFilename 清洗上传文件名，去掉路径与危险字符。
func sanitizeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = unsafeCharsRe.ReplaceAllString(name, "_")
	if name == "" || name == "." || name == ".." {
		name = "file"
	}
	return name
}

func (f *fileStore) SaveFile(ctx context.Context, category, filename string, r io.Reader) (string, error) {
	_ = ctx
	if !IsValidIdentifier(category) {
		return "", errors.New("invalid category name")
	}
	filename = sanitizeFilename(filename)
	if !isImageFile(filename) {
		return "", fmt.Errorf("不支持的文件类型: %s", filename)
	}
	dir := filepath.Join(f.dir, category)
	if _, err := os.Stat(dir); err != nil {
		return "", errors.New("分类不存在")
	}
	// 重名时追加随机后缀
	name := filename
	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			break
		}
		name = fmt.Sprintf("%s_%d%s", base, rand.IntN(1<<30), ext)
	}
	dst, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, r); err != nil {
		os.Remove(filepath.Join(dir, name))
		return "", err
	}
	if err := f.reload(); err != nil {
		return "", err
	}
	return name, nil
}

// SaveUploaded 便捷方法：保存 multipart 文件。
func (f *fileStore) SaveUploaded(ctx context.Context, category string, fh *multipart.FileHeader) (string, error) {
	src, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer src.Close()
	return f.SaveFile(ctx, category, fh.Filename, src)
}

func (f *fileStore) DeleteFiles(ctx context.Context, category string, names []string) (int, error) {
	_ = ctx
	if !IsValidIdentifier(category) {
		return 0, errors.New("invalid category name")
	}
	dir := filepath.Join(f.dir, category)
	if _, err := os.Stat(dir); err != nil {
		return 0, errors.New("分类不存在")
	}
	deleted := 0
	for _, name := range names {
		name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
		if name == "" || name == "." || name == ".." || !isImageFile(name) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err == nil {
			deleted++
		}
	}
	if err := f.reload(); err != nil {
		return deleted, err
	}
	return deleted, nil
}

// Dir 返回数据根目录（用于静态文件服务）。
func Dir(s Store) string {
	if fs, ok := s.(*fileStore); ok {
		return fs.dir
	}
	return ""
}

// DeleteCategory 删除整个分类目录（含全部图片文件）。
// 供后台分类删除功能使用；调用方须做好二次确认。
func (f *fileStore) DeleteCategory(ctx context.Context, name string) error {
	_ = ctx
	if !IsValidIdentifier(name) {
		return errors.New("invalid category name")
	}
	path := filepath.Join(f.dir, name)
	if _, err := os.Stat(path); err != nil {
		return errors.New("分类不存在")
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return f.reload()
}
