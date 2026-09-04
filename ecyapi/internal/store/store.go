// Package store 提供随机图片链接的数据访问：MySQL 优先，文件目录兜底。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql" // MySQL 驱动（纯 Go，无 cgo）
)

// ErrNotFound 表示分类不存在或分类内无有效链接。
var ErrNotFound = errors.New("not found")

// Row 一条图片记录。
type Row struct {
	ID   string
	Link string
}

var identifierRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

// IsValidIdentifier 校验表名/分类名，防注入。
func IsValidIdentifier(name string) bool { return identifierRe.MatchString(name) }

// Store 统一数据访问接口。
type Store interface {
	// Categories 返回分类名列表。
	Categories(ctx context.Context) ([]string, error)
	// Count 返回分类内有效链接数。
	Count(ctx context.Context, category string) (int, error)
	// Random 返回 limit 条随机记录。
	Random(ctx context.Context, category string, limit int) ([]Row, error)
	// ByIndex 按序号（1 起）返回一条记录。
	ByIndex(ctx context.Context, category string, index int) (Row, error)
	// Links 返回分类内全部链接（图库/后台用）。
	Links(ctx context.Context, category string) ([]Row, error)
}

// ---------------------------------------------------------------- 文件实现

type fileStore struct {
	dir string

	mu    sync.RWMutex
	cache map[string][]string // category -> links
}

// NewFileStore 使用目录内 <category>.txt（每行一条链接）。
func NewFileStore(dir string) (Store, error) {
	fs := &fileStore{dir: dir, cache: map[string][]string{}}
	if err := fs.reload(); err != nil {
		return nil, err
	}
	return fs, nil
}

func (f *fileStore) reload() error {
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return fmt.Errorf("read data dir %s: %w", f.dir, err)
	}
	next := map[string][]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".txt")
		if !IsValidIdentifier(name) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(f.dir, e.Name()))
		if err != nil {
			continue
		}
		var links []string
		for _, line := range strings.Split(string(data), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				links = append(links, line)
			}
		}
		if len(links) > 0 {
			next[name] = links
		}
	}
	f.mu.Lock()
	f.cache = next
	f.mu.Unlock()
	return nil
}

func (f *fileStore) categoryLinks(category string) ([]string, bool) {
	f.mu.RLock()
	links, ok := f.cache[category]
	f.mu.RUnlock()
	return links, ok
}

func (f *fileStore) Categories(ctx context.Context) ([]string, error) {
	_ = ctx
	if err := f.reload(); err != nil { // 每次重载，热更新文件
		return nil, err
	}
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
	links, ok := f.categoryLinks(category)
	if !ok {
		return 0, ErrNotFound
	}
	return len(links), nil
}

func (f *fileStore) Random(ctx context.Context, category string, limit int) ([]Row, error) {
	_ = ctx
	links, ok := f.categoryLinks(category)
	if !ok {
		return nil, ErrNotFound
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	perm := rng.Perm(len(links))
	if limit > len(perm) {
		limit = len(perm)
	}
	rows := make([]Row, 0, limit)
	for _, i := range perm[:limit] {
		rows = append(rows, Row{Link: links[i]})
	}
	return rows, nil
}

func (f *fileStore) ByIndex(ctx context.Context, category string, index int) (Row, error) {
	_ = ctx
	if index < 1 {
		return Row{}, ErrNotFound
	}
	links, ok := f.categoryLinks(category)
	if !ok || index > len(links) {
		return Row{}, ErrNotFound
	}
	return Row{Link: links[index-1]}, nil
}

func (f *fileStore) Links(ctx context.Context, category string) ([]Row, error) {
	_ = ctx
	links, ok := f.categoryLinks(category)
	if !ok {
		return nil, ErrNotFound
	}
	rows := make([]Row, 0, len(links))
	for i, l := range links {
		rows = append(rows, Row{ID: fmt.Sprintf("%d", i+1), Link: l})
	}
	return rows, nil
}

// ---------------------------------------------------------------- MySQL 实现

type mysqlStore struct {
	db *sql.DB
}

// NewMySQLStore 使用 MySQL（表结构：id 自增主键、link VARCHAR）。
func NewMySQLStore(dsn string) (Store, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(16)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return &mysqlStore{db: db}, nil
}

// DSN 由各字段拼装 MySQL DSN。
func DSN(host string, port int, user, password, name, charset string) string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=false&timeout=3s&readTimeout=5s",
		user, url.QueryEscape(password), host, port, name, charset)
}

func (m *mysqlStore) tableExists(ctx context.Context, table string) (bool, error) {
	var one int
	err := m.db.QueryRowContext(ctx,
		"SELECT 1 FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ? LIMIT 1",
		table).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (m *mysqlStore) Categories(ctx context.Context) ([]string, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT DISTINCT table_name FROM information_schema.columns
		 WHERE table_schema = DATABASE() AND column_name = 'link' ORDER BY table_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if IsValidIdentifier(name) {
			names = append(names, name)
		}
	}
	return names, rows.Err()
}

func (m *mysqlStore) count(ctx context.Context, table string) (int, error) {
	exists, err := m.tableExists(ctx, table)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, ErrNotFound
	}
	var n int
	err = m.db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM `%s` WHERE link IS NOT NULL AND link <> ''", table)).Scan(&n)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrNotFound
	}
	return n, nil
}

func (m *mysqlStore) Random(ctx context.Context, category string, limit int) ([]Row, error) {
	if !IsValidIdentifier(category) {
		return nil, ErrNotFound
	}
	total, err := m.count(ctx, category)
	if err != nil {
		return nil, err
	}
	if limit > total {
		limit = total
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	offsetSet := map[int]bool{}
	for len(offsetSet) < limit {
		offsetSet[rng.Intn(total)] = true
	}
	offsets := make([]int, 0, len(offsetSet))
	for o := range offsetSet {
		offsets = append(offsets, o)
	}
	sort.Ints(offsets)

	rows := make([]Row, 0, limit)
	for _, off := range offsets {
		var id, link string
		err := m.db.QueryRowContext(ctx,
			fmt.Sprintf("SELECT id, link FROM `%s` WHERE link IS NOT NULL AND link <> '' LIMIT 1 OFFSET ?", category),
			off).Scan(&id, &link)
		if err == nil && link != "" {
			rows = append(rows, Row{ID: id, Link: link})
		}
	}
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	return rows, nil
}

func (m *mysqlStore) Count(ctx context.Context, category string) (int, error) {
	if !IsValidIdentifier(category) {
		return 0, ErrNotFound
	}
	return m.count(ctx, category)
}

func (m *mysqlStore) ByIndex(ctx context.Context, category string, index int) (Row, error) {
	if !IsValidIdentifier(category) || index < 1 {
		return Row{}, ErrNotFound
	}
	exists, err := m.tableExists(ctx, category)
	if err != nil || !exists {
		return Row{}, ErrNotFound
	}
	var id, link string
	err = m.db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT id, link FROM `%s` WHERE link IS NOT NULL AND link <> '' LIMIT 1 OFFSET ?", category),
		index-1).Scan(&id, &link)
	if err != nil || link == "" {
		return Row{}, ErrNotFound
	}
	return Row{ID: id, Link: link}, nil
}

func (m *mysqlStore) Links(ctx context.Context, category string) ([]Row, error) {
	if !IsValidIdentifier(category) {
		return nil, ErrNotFound
	}
	exists, err := m.tableExists(ctx, category)
	if err != nil || !exists {
		return nil, ErrNotFound
	}
	rows, err := m.db.QueryContext(ctx,
		fmt.Sprintf("SELECT id, link FROM `%s` WHERE link IS NOT NULL AND link <> '' ORDER BY id ASC", category))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var id, link string
		if err := rows.Scan(&id, &link); err != nil {
			return nil, err
		}
		out = append(out, Row{ID: id, Link: link})
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- 后台写操作

// Admin 后台管理写操作接口。
type Admin interface {
	Store
	// UpsertLinks 全量覆盖分类链接。
	UpsertLinks(ctx context.Context, category string, links []string) error
	// CreateTable 新建分类表（仅 MySQL）。
	CreateTable(ctx context.Context, name string) error
}

type adminFile struct{ *fileStore }

// NewFileAdmin 包装文件存储以支持后台写操作。
func NewFileAdmin(s Store) (Admin, error) {
	fs, ok := s.(*fileStore)
	if !ok {
		return nil, errors.New("store is not file backed")
	}
	return &adminFile{fs}, nil
}

func (a *adminFile) UpsertLinks(ctx context.Context, category string, links []string) error {
	_ = ctx
	if !IsValidIdentifier(category) {
		return errors.New("invalid category name")
	}
	var b strings.Builder
	for _, l := range links {
		l = strings.TrimSpace(l)
		if l != "" {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	if err := os.WriteFile(filepath.Join(a.dir, category+".txt"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	return a.reload()
}

func (a *adminFile) CreateTable(ctx context.Context, name string) error {
	_ = ctx
	if !IsValidIdentifier(name) {
		return errors.New("invalid table name")
	}
	path := filepath.Join(a.dir, name+".txt")
	if _, err := os.Stat(path); err == nil {
		return errors.New("category already exists")
	}
	return os.WriteFile(path, nil, 0o644)
}

type adminMySQL struct{ *mysqlStore }

// NewMySQLAdmin 包装 MySQL 存储以支持后台写操作。
func NewMySQLAdmin(s Store) (Admin, error) {
	ms, ok := s.(*mysqlStore)
	if !ok {
		return nil, errors.New("store is not mysql backed")
	}
	return &adminMySQL{ms}, nil
}

func (a *adminMySQL) UpsertLinks(ctx context.Context, category string, links []string) error {
	if !IsValidIdentifier(category) {
		return errors.New("invalid category name")
	}
	exists, err := a.tableExists(ctx, category)
	if err != nil || !exists {
		return errors.New("table not found")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	table := "`" + category + "`"
	if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO "+table+" (link) VALUES (?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, l := range links {
		if l = strings.TrimSpace(l); l != "" {
			if _, err := stmt.ExecContext(ctx, l); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (a *adminMySQL) CreateTable(ctx context.Context, name string) error {
	if !IsValidIdentifier(name) {
		return errors.New("invalid table name")
	}
	exists, err := a.tableExists(ctx, name)
	if err != nil {
		return err
	}
	if exists {
		return errors.New("table already exists")
	}
	_, err = a.db.ExecContext(ctx, fmt.Sprintf(
		"CREATE TABLE `%s` (id INT AUTO_INCREMENT PRIMARY KEY, link VARCHAR(1000) NOT NULL, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4", name))
	return err
}

// IsValidIdentifierName 供其它包复用的分类名校验。
func IsValidIdentifierName(name string) bool { return identifierRe.MatchString(name) }
