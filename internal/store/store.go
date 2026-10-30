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
	"log"
	"math/rand/v2"
	"mime/multipart"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// ErrNotFound 表示分类不存在或分类内无有效图片。
var ErrNotFound = errors.New("not found")

// imageExts 允许作为图片/动图资源访问的扩展名。
var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
	".webp": true, ".bmp": true, ".avif": true, ".svg": true,
	".mp4": true, ".webm": true,
}

// videoExts 视频扩展名：不支持 ?w= 压缩预览，需走流式输出。
var videoExts = map[string]bool{
	".mp4": true, ".webm": true,
}

// IsVideoFile 判断文件名是否为视频。
func IsVideoFile(name string) bool {
	return videoExts[strings.ToLower(filepath.Ext(name))]
}

// 分类支持多级子目录：data/categories/<段1>/<段2>/.../<段N>，
// 对外即分类路径 "段1/段2"（如 /pic/a/b/f.png 对应分类 a/b）。
// 层级与总长设上限，防超深目录与超长路径。
const (
	// DefaultMaxDepth 默认分类层级上限，可用环境变量 ECY_MAX_DEPTH 调整。
	DefaultMaxDepth = 3
	// MaxDepthLimit 允许配置的层级上限：再深只会增加扫描成本，
	// 不支持无限层级是刻意的（防止误配把整棵目录树当分类扫）。
	MaxDepthLimit  = 8
	maxCategoryLen = 512
	maxSegmentLen  = 128 // 单段字节数上限（中文约 42 字）
)

// reservedTop 与内置路由冲突的首段名：这类目录不会被载入为分类，
// 也不会被后台创建，避免 /pic、/json 等被遮蔽。
var reservedTop = map[string]bool{
	"pic": true, "json": true, "admin": true, "img": true,
	"docs": true, "healthz": true, "favicon.ico": true, "readyz": true,
	// 全分类随机入口（等价于往所有分类里抽一个），见 server.handleRoot。
	"@img": true,
	// 内置静态资源（看板娘脚本与素材），见 server.handleIndexStatic。
	"@index": true,
}

// IsReservedTop 判断分类路径的首段是否与内置路由冲突。
func IsReservedTop(name string) bool {
	if i := strings.IndexByte(name, '/'); i >= 0 {
		name = name[:i]
	}
	return reservedTop[name]
}

// IsValidSegment 校验单段目录名，规则尽量宽松但保证安全：
//
//   - 允许：字母、数字、中文等任意 UTF-8 字符，以及 _ - . + @ ( ) & , ' ! 空格
//     （例如 2024-01、pixiv.1、KuroiDa、中文目录、A B）
//   - 拒绝：空段、"."、".."、以 "." 开头（隐藏目录）、含 / 或 \、
//     控制字符、URL 语义字符 ? # %、首尾空格与结尾的点
//   - 单段 ≤ maxSegmentLen 字节
//
// 只要不含 / 与 ".."，拼接后就不可能穿越到数据根目录之外。
func IsValidSegment(name string) bool {
	if name == "" || len(name) > maxSegmentLen {
		return false
	}
	if name == "." || name == ".." {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return false // 隐藏目录（.git/.Trash 等）不作为分类
	}
	if strings.HasPrefix(name, " ") || strings.HasSuffix(name, " ") {
		return false
	}
	if strings.HasSuffix(name, ".") {
		return false // Windows 上非法，URL 里也容易被误处理
	}
	if !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		switch {
		case r == '/' || r == '\\':
			return false
		case r < 0x20 || r == 0x7f:
			return false // 控制字符
		case r == '?' || r == '#' || r == '%':
			return false // URL 保留字符，避免路径歧义
		}
	}
	return true
}

// IsValidIdentifier 兼容旧名：等价于 IsValidSegment（单段校验）。
func IsValidIdentifier(name string) bool { return IsValidSegment(name) }

// IsValidCategory 校验分类路径（使用默认层级上限）。
func IsValidCategory(name string) bool {
	return IsValidCategoryDepth(name, DefaultMaxDepth)
}

// IsValidCategoryDepth 校验分类路径：1~maxDepth 段，以 "/" 分隔。
// 每段走 IsValidSegment；首段不能是内置路由保留名。
// 空串、空段（a//b）、"."、".."、反斜杠、绝对路径（/a）一律拒绝。
func IsValidCategoryDepth(name string, maxDepth int) bool {
	if maxDepth < 1 {
		maxDepth = DefaultMaxDepth
	}
	if maxDepth > MaxDepthLimit {
		maxDepth = MaxDepthLimit
	}
	if name == "" || len(name) > maxCategoryLen {
		return false
	}
	segs := strings.Split(name, "/")
	if len(segs) > maxDepth {
		return false
	}
	for _, seg := range segs {
		if !IsValidSegment(seg) {
			return false
		}
	}
	return !IsReservedTop(name)
}

// CategorySegments 返回分类路径的各段；非法输入返回 nil。
func CategorySegments(name string, maxDepth int) []string {
	if !IsValidCategoryDepth(name, maxDepth) {
		return nil
	}
	return strings.Split(name, "/")
}

// IsValidIdentifierName 兼容旧调用：等价于 IsValidCategory。
func IsValidIdentifierName(name string) bool { return IsValidCategory(name) }

// Row 一条图片记录。Link 为站内可访问的图片地址。
type Row struct {
	ID   string // 文件名
	Link string // 站内访问地址，如 /pic/pc/xxx.webp

	// 以下字段由扫描时读文件头得到，供后台预览与横竖筛选使用；
	// 视频 / SVG / AVIF 等无法判定宽高的资源 Orientation 为 unknown。
	Width       int
	Height      int
	Orientation string // landscape / portrait / square / unknown
}

// Store 统一数据访问接口。
// Count/Random/ByIndex 等价于带 OrientationAny 的 With 版本。
type Store interface {
	Categories(ctx context.Context) ([]string, error)
	Count(ctx context.Context, category string) (int, error)
	Random(ctx context.Context, category string, limit int) ([]Row, error)
	ByIndex(ctx context.Context, category string, index int) (Row, error)
	Links(ctx context.Context, category string) ([]Row, error)

	// CountWith 统计指定方向的文件数。
	CountWith(ctx context.Context, category string, want Orientation) (int, error)
	// RandomWith 只在指定方向的子集中随机抽取。
	RandomWith(ctx context.Context, category string, limit int, want Orientation) ([]Row, error)
	// ByIndexWith 在指定方向的子集中按序号（字典序，从 1 起）取图。
	ByIndexWith(ctx context.Context, category string, index int, want Orientation) (Row, error)
	// LinksPage 分页返回图片列表，避免大分类一次性复制全部记录。
	LinksPage(ctx context.Context, category string, offset, limit int) (rows []Row, total int, err error)
	// Verify 校验文件是否仍在磁盘上；不存在时从内存索引里摘掉并返回 false，
	// 供 302 前做一次「目标是否还活着」的确认。
	Verify(ctx context.Context, category, name string) bool
	// Generation 文件索引的代次：每次扫描发现变化就 +1，用于缓存失效。
	Generation() uint64
	// Scanning 是否正在扫描磁盘（后台预热据此让路）。
	Scanning() bool
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

// fileEntry 分类内的一张图片（含扫描时判定的宽高与方向）。
type fileEntry struct {
	name string
	link string
	w, h int
	ori  Orientation
}

// Row 转换：对外统一填充方向字段。
func (e fileEntry) row() Row {
	return Row{ID: e.name, Link: e.link, Width: e.w, Height: e.h, Orientation: e.ori.String()}
}

// categorySet 一个分类的图片集合。
// 横竖在扫描时就分好桶，读路径直接取切片，不必每次过滤分配。
type categorySet struct {
	all  []fileEntry
	land []fileEntry // 横版
	port []fileEntry // 竖版
}

func (c *categorySet) add(e fileEntry) {
	c.all = append(c.all, e)
	switch e.ori {
	case OrientationLandscape:
		c.land = append(c.land, e)
	case OrientationPortrait:
		c.port = append(c.port, e)
	}
}

// sort 按文件名（字典序）排序，保证 ?json=N 的序号稳定。
func (c *categorySet) sort() {
	byName := func(s []fileEntry) {
		sort.Slice(s, func(i, j int) bool { return s[i].name < s[j].name })
	}
	byName(c.all)
	byName(c.land)
	byName(c.port)
}

// pick 取指定方向的桶；Any 返回全集。
// 正方形与无法判定的文件只进全集，不参与横竖筛选，因此这里返回nil由调用方回退。
func (c *categorySet) pick(want Orientation) []fileEntry {
	switch want {
	case OrientationLandscape:
		return c.land
	case OrientationPortrait:
		return c.port
	case OrientationSquare, OrientationUnknown:
		return nil
	}
	return c.all
}

// orientMeta 方向缓存条目：文件 mtime/size 未变时复用上次的宽高，
// 避免每轮重扫都把整库文件头重新读一遍。
type orientMeta struct {
	size int64
	mod  int64
	w, h int
	ori  Orientation
}

type fileStore struct {
	dir      string
	maxDepth int // 分类层级上限（ECY_MAX_DEPTH）

	// reloadMu 串行化扫描（后台刷新与写路径 reload 共用）。
	reloadMu sync.Mutex
	// refreshInterval 后台刷新间隔（0 表示不启动后台刷新，仅供测试）。
	refreshInterval time.Duration

	mu    sync.RWMutex
	cache map[string]*categorySet // category -> 图片集合

	// oriMeta 方向缓存：分类路径 -> 文件名 -> 上次判定结果。
	// 与 dirs 一样只在 scan（持 reloadMu）中读写，无需额外加锁。
	oriMeta map[string]map[string]orientMeta
	// dirs 目录扫描状态：rel -> 上次条目名 / 子目录 / 文件集合，用于整目录跳过。
	dirs map[string]*dirState
	// verifyInterval 强制完整校验间隔（ECY_VERIFY_INTERVAL）。
	verifyInterval time.Duration
	// parentAggDirsOnly 父目录聚合模式：true = 仅对「自身没有图片」的父目录聚合；
	// false（默认）= 自身有图片也把子目录聚合进来（ECY_PARENT_AGG=dirs_only 可关闭）。
	parentAggDirsOnly bool
	// forceVerify 下一次 scan 强制完整校验（写路径的 reload 使用），
	// 保证上传/删除后即使条目名未变也能立即反映文件内容变化。
	forceVerify bool
	// skipped 最近一次扫描中被跳过的目录与原因，供启动日志排查
	// 「为什么某个子文件夹没有出现在分类里」。
	skipped []string
	// aggs 父目录聚合：rel -> 子分类集合的加权视图，只用于 URL/JSON 取图，
	// 不进入分区列表（避免图库与后台重复计数）。与 cache 同锁保护。
	aggs map[string]*aggKids
	// gen 索引代次：扫描发现任何目录变化就 +1，供图库 HTML 之类的外层缓存失效。
	gen atomic.Uint64
	// scanning 是否有扫描在进行（供后台预热让路、健康检查展示）。
	scanning atomic.Bool
	// scannedOnce 首次扫描是否已完成（首次扫描会分批发布部分结果）。
	scannedOnce atomic.Bool
	// dirty 自上次写快照以来索引是否有变化。
	dirty atomic.Bool
	// snapshotErrLogged 写快照失败是否已提示过（避免只读部署刷屏）。
	snapshotErrLogged atomic.Bool
}

// Scanning 是否正在扫描磁盘。
func (f *fileStore) Scanning() bool { return f.scanning.Load() }

// Generation 返回当前索引代次。
func (f *fileStore) Generation() uint64 { return f.gen.Load() }

// NewFileStore 使用默认层级上限（DefaultMaxDepth）创建存储。
func NewFileStore(dir string) (Store, error) {
	return NewFileStoreDepth(dir, DefaultMaxDepth)
}

// Options 存储初始化参数。
type Options struct {
	// MaxDepth 分类层级上限（ECY_MAX_DEPTH），0 取默认值。
	MaxDepth int
	// ScanInterval 后台刷新间隔（ECY_SCAN_INTERVAL），0 取默认 3 秒。
	// 扫描不再发生在请求路径上：请求只读快照，后台按此间隔增量刷新。
	ScanInterval time.Duration
	// StartupWait 首次扫描的等待上限（ECY_STARTUP_WAIT，默认 15 秒）。
	// 超时后先对外提供服务（返回已扫描到的部分），扫描在后台继续；
	// 0 表示阻塞到扫描完成。
	StartupWait time.Duration
	// ParentAggDirsOnly 父目录聚合模式：true = 仅聚合「自身没有图片」的父目录；
	// false（默认）= 自身有图片时也把子目录聚合进来（ECY_PARENT_AGG=with_files）。
	ParentAggDirsOnly bool
	// VerifyInterval 目录条目未变化时，强制完整校验的间隔（ECY_VERIFY_INTERVAL，默认 1 小时）。
	// 用于发现「同名文件被原地替换」；库越大该值应越大（每次都要 stat 全部文件）。
	VerifyInterval time.Duration
	// SnapshotTTL 运行期把内存索引落盘的周期（ECY_SNAPSHOT_TTL，默认 5 分钟）。
	// 每个周期无条件重写一次，保证重启时快照总是新鲜的。0 表示关闭快照。
	SnapshotTTL time.Duration
	// SnapshotMaxAge 启动时接受的最大快照年龄（ECY_SNAPSHOT_MAX_AGE，默认 30 天）。
	// 快照再旧也只是「启动时的初值」，随后 3 秒一次的增量刷新会按文件名把增删纠正过来，
	// 所以这里给得很宽。0 表示不做过期判断。
	SnapshotMaxAge time.Duration
}

// NewFileStoreDepth 指定分类层级上限创建存储（ECY_MAX_DEPTH）。
func NewFileStoreDepth(dir string, maxDepth int) (Store, error) {
	return NewFileStoreWithOptions(dir, Options{MaxDepth: maxDepth})
}

// NewFileStoreWithOptions 按给定参数创建存储，并启动后台刷新协程。
func NewFileStoreWithOptions(dir string, opt Options) (Store, error) {
	maxDepth := opt.MaxDepth
	if maxDepth < 1 {
		maxDepth = DefaultMaxDepth
	}
	if maxDepth > MaxDepthLimit {
		maxDepth = MaxDepthLimit
	}
	interval := opt.ScanInterval
	if interval <= 0 {
		interval = defaultScanInterval
	}
	verify := opt.VerifyInterval
	if verify <= 0 {
		verify = defaultVerifyInterval
	}
	fs := &fileStore{
		dir:               dir,
		maxDepth:          maxDepth,
		refreshInterval:   interval,
		verifyInterval:    verify,
		parentAggDirsOnly: opt.ParentAggDirsOnly,
		cache:             map[string]*categorySet{},
		oriMeta:           map[string]map[string]orientMeta{},
		dirs:              map[string]*dirState{},
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir %s: %w", dir, err)
	}
	// 1) 先尝试磁盘快照：命中就不扫盘，毫秒级就绪。
	if opt.SnapshotTTL > 0 {
		ok, err := fs.loadSnapshot(opt.SnapshotMaxAge)
		if err != nil {
			log.Printf("快照不可用，改为全量扫描：%v", err)
		} else if ok {
			log.Printf("已从磁盘快照恢复 %d 个分类（保存于 %s 前），跳过启动扫描",
				fs.loadedCategories(), time.Since(snapshotTime(fs.snapshotPath())).Round(time.Minute))
			go fs.refreshLoop()
			go fs.snapshotLoop(opt.SnapshotTTL)
			return fs, nil
		}
	}

	// 2) 首次扫描放后台：大库全量扫描可能几十秒，不能让进程一直不监听。
	fs.scanning.Store(true)
	scanDone := make(chan error, 1)
	go func() {
		err := fs.scan()
		fs.scanning.Store(false)
		// 首次扫描完成后立刻落一份快照：新部署的下一次重启就能秒起。
		if err == nil && opt.SnapshotTTL > 0 {
			if serr := fs.saveSnapshot(); serr != nil {
				log.Printf("写快照失败: %v", serr)
			}
		}
		scanDone <- err
	}()
	if opt.StartupWait > 0 {
		select {
		case err := <-scanDone:
			if err != nil {
				return nil, err
			}
		case <-time.After(opt.StartupWait):
			log.Printf("首次扫描超过 %s 仍未完成：先对外提供服务（已加载 %d 个分类），扫描在后台继续",
				opt.StartupWait, fs.loadedCategories())
		}
	} else {
		if err := <-scanDone; err != nil {
			return nil, err
		}
	}
	go fs.refreshLoop()
	go fs.snapshotLoop(opt.SnapshotTTL)
	return fs, nil
}

// snapshotTime 快照文件的修改时间（用于日志展示「多久之前的快照」）。
func snapshotTime(path string) time.Time {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime()
	}
	return time.Now()
}

// loadedCategories 当前已发布到读路径的分类数量（扫描进度示意）。
func (f *fileStore) loadedCategories() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.cache)
}

// defaultScanInterval 后台刷新默认间隔。
const defaultScanInterval = 3 * time.Second

// refreshLoop 后台增量刷新：请求路径不再触发扫描，
// 避免「每 3 秒一次的尾延迟尖峰」（5 万张约 62ms）。
func (f *fileStore) refreshLoop() {
	t := time.NewTicker(f.refreshInterval)
	defer t.Stop()
	for range t.C {
		// 数据目录被删掉（测试临时目录回收、误删挂载点）时停止刷新，
		// 避免日志被无意义的失败刷屏。
		if _, err := os.Stat(f.dir); err != nil {
			return
		}
		f.reloadMu.Lock()
		f.scanning.Store(true)
		err := f.scan()
		f.scanning.Store(false)
		f.reloadMu.Unlock()
		if err != nil {
			log.Printf("后台刷新失败: %v", err)
		}
	}
}

func isImageFile(name string) bool {
	return imageExts[strings.ToLower(filepath.Ext(name))]
}

// dirState 一个目录上次扫描的状态。
// 目录内条目名集合未变时直接复用上次结果，避免每 3 秒把整库文件 stat 一遍；
// 每 verifyInterval（ECY_VERIFY_INTERVAL，默认 1 小时）仍强制完整校验一次，
// 以捕获「同名文件被原地替换」。
type dirState struct {
	names    []string     // 上次的条目名（ReadDir 已按文件名排序）
	subdirs  []string     // 已校验的子目录名
	set      *categorySet // 该目录作为分类时的文件集合；纯承载目录为 nil
	verified int64        // 上次完整校验时间（UnixNano）
	skipped  []string     // 本目录下被跳过的子目录与原因
}

// defaultVerifyInterval 目录条目未变化时，两次完整 stat+方向校验的默认间隔（1 小时）。
// 只影响「同名文件被原地覆盖」这一种情况的生效延迟，正常增删改立即生效。
// 每次完整校验要把该目录下所有文件 stat 一遍，大库上很贵，因此默认给到 1 小时；
// 可用 ECY_VERIFY_INTERVAL 调整。
const defaultVerifyInterval = time.Hour

// scanCtx 一次扫描的上下文（避免 scanDir 参数越传越长）。
type scanCtx struct {
	next    map[string]*dirState
	sets    map[string]*categorySet
	aggs    map[string]*aggKids
	seen    map[string]bool // 已扫描过的真实目录路径，用于符号链接回环/重复检测
	skipped []string        // 被跳过的目录与原因，供启动日志诊断
	changed bool            // 本次扫描是否有目录被重新读取（用于代次自增）

	// 首次扫描时分批发布部分结果，让服务能先对外提供已有数据。
	progressive bool
	lastPublish time.Time
	lastLog     time.Time
	// scanned 本次扫描实际读过的目录（用于统计与日志）。
	scanned []string
	// rebuilt 本次扫描真正重建（走了慢路径）的目录数。
	rebuilt int
	// rebuiltDirs 前几个被重建的目录名（日志用，最多 5 个）。
	rebuiltDirs []string
}

// maxScanWarnings 启动日志里最多展示多少条「跳过原因」。
const maxScanWarnings = 50

// scan 扫描数据目录并整体替换缓存。
//
// 增量策略：每个目录只做一次 ReadDir 取条目名，与上次比较：
//   - 条目名完全一致且距上次完整校验不足 verifyInterval：整目录复用上次结果；
//   - 否则对该目录做完整校验（逐文件 stat + 必要时读文件头判定方向）。
//
// 因此未发生变更时的重扫代价是「每层目录一次 getdents + 名字比较」，
// 与图片数量弱相关（不产生每文件 syscall），3000 张图约 0.2ms。
func (f *fileStore) scan() error {
	start := time.Now()
	before := f.categoryNames()
	sc := &scanCtx{
		next: make(map[string]*dirState, len(f.dirs)),
		sets: make(map[string]*categorySet, len(f.cache)),
		aggs: make(map[string]*aggKids, len(f.aggs)),
		seen: map[string]bool{filepath.Clean(f.dir): true},
		// 首次扫描（库还空着）分批发布，避免「要么全有、要么全无」。
		progressive: !f.scannedOnce.Load(),
	}
	if err := f.scanDir("", 0, sc, true); err != nil {
		return err
	}
	// 清理已消失目录的方向缓存，避免长期运行后 map 只增不减。
	for rel := range f.oriMeta {
		if _, ok := sc.next[rel]; !ok {
			delete(f.oriMeta, rel)
		}
	}
	// 跳过项去重并限长：同名原因只报一次，避免启动日志被刷屏。
	seenMsg := make(map[string]bool, len(sc.skipped))
	warns := make([]string, 0, min(len(sc.skipped), maxScanWarnings))
	for _, w := range sc.skipped {
		if seenMsg[w] {
			continue
		}
		seenMsg[w] = true
		warns = append(warns, w)
		if len(warns) >= maxScanWarnings {
			break
		}
	}
	f.mu.Lock()
	f.cache = sc.sets
	f.aggs = sc.aggs
	f.skipped = warns
	f.mu.Unlock()
	f.dirs = sc.next
	f.scannedOnce.Store(true)
	if sc.changed {
		f.gen.Add(1)
		f.dirty.Store(true)
	}
	f.logScanResult(before, sc, time.Since(start))
	return nil
}

// categoryNames 当前分类名集合（用于扫描前后对比）。
func (f *fileStore) categoryNames() map[string]bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	m := make(map[string]bool, len(f.cache))
	for k := range f.cache {
		m[k] = true
	}
	return m
}

// logScanResult 输出一次扫描的摘要：
//   - 首次扫描：分类数、读了几个目录、耗时、耗时在哪；
//   - 增量刷新：只在真有变化时打印「新增/移除/重建」明细，避免每 3 秒刷屏。
func (f *fileStore) logScanResult(before map[string]bool, sc *scanCtx, dur time.Duration) {
	added := make([]string, 0, 4)
	removed := make([]string, 0, 4)
	for name := range sc.sets {
		if !before[name] {
			added = append(added, name)
		}
	}
	for name := range before {
		if _, ok := sc.sets[name]; !ok {
			removed = append(removed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)

	detail := ""
	if len(added) > 0 {
		detail += " 新增 " + summarizeNames(added)
	}
	if len(removed) > 0 {
		detail += " 移除 " + summarizeNames(removed)
	}

	if sc.progressive {
		log.Printf("首次扫描完成：%d 个分类，读取 %d 个目录，耗时 %s%s",
			len(sc.sets), len(sc.scanned), fmtDuration(dur), detail)
		return
	}
	if !sc.changed {
		return // 无变化不打印，避免每 3 秒一行
	}
	where := ""
	if len(sc.rebuiltDirs) > 0 {
		where = "（" + summarizeNames(sc.rebuiltDirs) + "）"
	}
	log.Printf("索引更新：重建 %d 个目录%s，耗时 %s%s（共 %d 个分类）",
		sc.rebuilt, where, fmtDuration(dur), detail, len(sc.sets))
}

// fmtDuration 让亚毫秒的扫描不至于显示成「0s」。
func fmtDuration(d time.Duration) string {
	if d < time.Millisecond {
		return strconv.FormatInt(int64(d/time.Microsecond), 10) + "µs"
	}
	if d < time.Second {
		return strconv.FormatInt(int64(d/time.Millisecond), 10) + "ms"
	}
	return d.Round(10 * time.Millisecond).String()
}

// summarizeNames 名字太多时只列前几个，其余折叠成计数。
func summarizeNames(names []string) string {
	const max = 3
	out := ""
	for i, n := range names {
		if i >= max {
			out += " 等 " + strconv.Itoa(len(names)) + " 个"
			break
		}
		if i > 0 {
			out += "、"
		}
		out += n
	}
	return out
}

// publishPartial 把「已扫完的那部分」发布出去（浅拷贝，底层集合不可变）。
// 只用于首次扫描：刷新扫描必须保持「要么旧的全量、要么新的全量」。
func (f *fileStore) publishPartial(sc *scanCtx) {
	sets := make(map[string]*categorySet, len(sc.sets))
	for k, v := range sc.sets {
		sets[k] = v
	}
	aggs := make(map[string]*aggKids, len(sc.aggs))
	for k, v := range sc.aggs {
		aggs[k] = v
	}
	f.mu.Lock()
	f.cache = sets
	f.aggs = aggs
	f.mu.Unlock()
}

// dirEntryKind 判断条目是否为目录，并区分是否通过符号链接。
// 符号链接指向目录时视为目录：把外部图库 ln -s 进来的用法很常见。
func dirEntryKind(dirPath string, e os.DirEntry) (isDir, isLink bool) {
	if e.IsDir() {
		return true, false
	}
	if e.Type()&os.ModeSymlink == 0 {
		return false, false
	}
	fi, err := os.Stat(filepath.Join(dirPath, e.Name()))
	if err != nil || !fi.IsDir() {
		return false, true
	}
	return true, true
}

// scanDir 扫描目录 rel（"" 表示数据根目录）。
// depth 为 rel 的段数（根为 0）；isRoot 为真时读目录失败向上返回错误。
func (f *fileStore) scanDir(rel string, depth int, sc *scanCtx, isRoot bool) error {
	dirPath := f.dir
	if rel != "" {
		dirPath = filepath.Join(f.dir, filepath.FromSlash(rel))
	}
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if isRoot {
			return fmt.Errorf("read data dir %s: %w", f.dir, err)
		}
		return nil
	}
	now := time.Now().UnixNano()
	old := f.dirs[rel]
	if old != nil && !f.forceVerify && now-old.verified < int64(f.verifyInterval) && sameEntryNames(old.names, entries) {
		sc.next[rel] = old
		if old.set != nil {
			sc.sets[rel] = old.set
		}
		sc.skipped = append(sc.skipped, old.skipped...)
		sc.scanned = append(sc.scanned, rel)
		scanned := make([]string, 0, len(old.subdirs))
		for _, sub := range old.subdirs {
			if err := f.scanDir(joinRel(rel, sub), depth+1, sc, false); err != nil {
				return err
			}
			scanned = append(scanned, sub)
			f.progressiveAgg(rel, scanned, sc)
		}
		f.buildAgg(rel, old.subdirs, sc)
		return nil
	}

	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	sc.changed = true // 走到这里说明该目录被重新读取（新增/删除/尚未扫过）
	sc.rebuilt++      // 统计真正重建的目录数（用于日志）
	if len(sc.rebuiltDirs) < 5 && rel != "" {
		sc.rebuiltDirs = append(sc.rebuiltDirs, rel)
	}
	state := &dirState{names: names, verified: now}
	if !isRoot {
		state.set = f.buildSet(rel, dirPath, entries)
		if state.set != nil {
			sc.sets[rel] = state.set
			// 首次扫描：每 300ms 把已完成的部分发布一次。
			if sc.progressive && time.Since(sc.lastPublish) > 300*time.Millisecond {
				f.publishPartial(sc)
				sc.lastPublish = time.Now()
			}
		}
	}
	if depth < f.maxDepth {
		for _, e := range entries {
			seg := e.Name()
			childRel := joinRel(rel, seg)
			isDir, isLink := dirEntryKind(dirPath, e)
			if !isDir {
				if isLink {
					state.skipped = append(state.skipped,
						childRel+"：符号链接指向的不是目录（或已失效）")
				}
				continue
			}
			if !IsValidSegment(seg) {
				state.skipped = append(state.skipped,
					childRel+"：目录名不合法（不能为空、不能以 . 开头、不能含斜杠或 ? # % 与控制字符，≤128 字节）")
				continue
			}
			if depth == 0 && IsReservedTop(childRel) {
				state.skipped = append(state.skipped,
					childRel+"：与内置接口路径冲突（保留名 pic/json/admin/img/docs/healthz/favicon.ico）")
				continue
			}
			if len(childRel) > maxCategoryLen {
				state.skipped = append(state.skipped, childRel+"：路径过长")
				continue
			}
			abs := filepath.Join(f.dir, filepath.FromSlash(childRel))
			if isLink {
				resolved, rerr := filepath.EvalSymlinks(abs)
				if rerr != nil {
					state.skipped = append(state.skipped, childRel+"：符号链接无法解析")
					continue
				}
				if sc.seen[resolved] {
					state.skipped = append(state.skipped, childRel+"：符号链接回环或重复指向")
					continue
				}
				sc.seen[resolved] = true
			} else {
				sc.seen[filepath.Clean(abs)] = true
			}
			state.subdirs = append(state.subdirs, seg)
		}
	}
	sc.skipped = append(sc.skipped, state.skipped...)
	sc.next[rel] = state
	sc.scanned = append(sc.scanned, rel)
	scanned := make([]string, 0, len(state.subdirs))
	for _, sub := range state.subdirs {
		if err := f.scanDir(joinRel(rel, sub), depth+1, sc, false); err != nil {
			return err
		}
		scanned = append(scanned, sub)
		// 首次扫描：每完成一个子目录就把父目录的部分聚合发出去，
		// 让 /pixiv 这类父路径不必等整棵子树扫完才可用（HDD 上差别很大）。
		f.progressiveAgg(rel, scanned, sc)
	}
	f.buildAgg(rel, state.subdirs, sc)
	return nil
}

// progressiveAgg 首次扫描期间增量构建父目录聚合视图并发布。
// 已有部分子目录就先用部分数据建视图，后续子目录扫完时再重建覆盖。
func (f *fileStore) progressiveAgg(rel string, scanned []string, sc *scanCtx) {
	if !sc.progressive || len(scanned) == 0 {
		return
	}
	if time.Since(sc.lastPublish) < 300*time.Millisecond {
		return
	}
	f.buildAgg(rel, scanned, sc)
	f.publishPartial(sc)
	sc.lastPublish = time.Now()
	if time.Since(sc.lastLog) > 5*time.Second {
		sc.lastLog = time.Now()
		log.Printf("首次扫描进行中：已加载 %d 个分类（当前 %s）", len(sc.sets), rel)
	}
}

// buildAgg 为「只有子目录、自身没有图片」的父目录建立聚合视图，
// 让 /pixiv 这类父路径也能随机出图。根目录不参与。
func (f *fileStore) buildAgg(rel string, subdirs []string, sc *scanCtx) {
	if rel == "" || len(subdirs) == 0 {
		return
	}
	own, isCategory := sc.sets[rel]
	if isCategory && f.parentAggDirsOnly {
		return // dirs_only 模式：自己有图片就是普通分类，不聚合子目录
	}
	kids := make([]string, 0, len(subdirs))
	for _, sub := range subdirs {
		kids = append(kids, joinRel(rel, sub))
	}
	var agg *aggKids
	if isCategory {
		agg = newAggKidsWithSelf(kids, sc.sets, sc.aggs, own) // with_files：自己的图 + 子目录
	} else {
		agg = newAggKids(kids, sc.sets, sc.aggs)
	}
	if agg != nil {
		sc.aggs[rel] = agg
	}
}

// sameEntryNames 比较上次记录的条目名与本次 ReadDir 结果（两者均按名字排序）。
func sameEntryNames(prev []string, cur []os.DirEntry) bool {
	if len(prev) != len(cur) {
		return false
	}
	for i, e := range cur {
		if prev[i] != e.Name() {
			return false
		}
	}
	return true
}

// joinRel 拼接相对路径（根目录的 rel 为空串）。
func joinRel(rel, seg string) string {
	if rel == "" {
		return seg
	}
	return rel + "/" + seg
}

// buildSet 收集目录内的图片并判定方向；返回 nil 表示该目录不登记为分类
// （纯承载目录：自身没有图片且有子分类）。
func (f *fileStore) buildSet(rel, dirPath string, entries []os.DirEntry) *categorySet {
	oldMeta := f.oriMeta[rel]
	meta := make(map[string]orientMeta, len(entries))
	set := &categorySet{}
	childDirs := 0

	imgs := make([]imgEntry, 0, len(entries))
	for _, e := range entries {
		if isDir, _ := dirEntryKind(dirPath, e); isDir {
			if IsValidIdentifier(e.Name()) {
				childDirs++
			}
			continue
		}
		name := e.Name()
		if !isImageFile(name) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			// 拿不到 stat 就退回「读一次文件头」，保持与旧行为一致。
			imgs = append(imgs, imgEntry{name: name, need: true})
			continue
		}
		ie := imgEntry{name: name, size: info.Size(), mod: info.ModTime().UnixNano()}
		m, ok := oldMeta[name]
		// mtime + size 都没变就复用上次判定，避免重复读文件头。
		if !ok || m.size != ie.size || m.mod != ie.mod {
			ie.need = true
		}
		imgs = append(imgs, ie)
	}

	// 需要读文件头的文件并发探测：机械盘上顺序/并行读能明显减少寻道等待，
	// SSD 上也能缩短冷启动时间。每个 goroutine 只处理自己那一批，结果按下标写回。
	var results []orientMeta
	if anyNeed(imgs) {
		results = f.detectParallel(dirPath, imgs)
	}

	for i, ie := range imgs {
		entry := fileEntry{name: ie.name, link: "/pic/" + rel + "/" + ie.name, ori: OrientationUnknown}
		m := oldMeta[ie.name]
		if results != nil && ie.need {
			m = results[i]
		}
		if m.size != 0 || m.ori != OrientationUnknown || m.w != 0 || m.h != 0 {
			meta[ie.name] = m
			entry.w, entry.h, entry.ori = m.w, m.h, m.ori
		}
		set.add(entry)
	}
	set.sort()
	if len(set.all) == 0 && childDirs > 0 {
		f.oriMeta[rel] = meta
		return nil
	}
	if len(meta) > 0 {
		f.oriMeta[rel] = meta
	} else {
		delete(f.oriMeta, rel)
	}
	return set
}

// imgEntry buildSet 内部使用：一个待处理的图片文件。
type imgEntry struct {
	name string
	size int64
	mod  int64
	need bool // 需要重新读文件头
}

// detectMaxWorkers 并发探测文件头的上限。
// 机械盘上 4 个已经足够让内核重排请求；再多只增加随机寻道。
const detectMaxWorkers = 4

// detectParallel 并发读取一组图片的文件头，返回与 imgs 等长的结果（需要的才算，其余为零值）。
func (f *fileStore) detectParallel(dirPath string, imgs []imgEntry) []orientMeta {
	out := make([]orientMeta, len(imgs))
	jobs := make(chan int)
	var wg sync.WaitGroup
	workers := detectMaxWorkers
	if n := runtime.GOMAXPROCS(0); n < workers {
		workers = n
	}
	if workers < 1 {
		workers = 1
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				ie := imgs[i]
				w, h, o := detectOrientation(filepath.Join(dirPath, ie.name))
				out[i] = orientMeta{size: ie.size, mod: ie.mod, w: w, h: h, ori: o}
			}
		}()
	}
	for i := range imgs {
		if imgs[i].need {
			jobs <- i
		}
	}
	close(jobs)
	wg.Wait()
	return out
}

// anyNeed 判断是否有文件需要重新读头。
func anyNeed(imgs []imgEntry) bool {
	for _, ie := range imgs {
		if ie.need {
			return true
		}
	}
	return false
}

// reload 同步强制重载（写路径使用）：跳过增量复用并强制完整校验。
// 请求路径不再调用扫描：日常刷新由后台 refreshLoop 负责。
func (f *fileStore) reload() error {
	f.reloadMu.Lock()
	defer f.reloadMu.Unlock()
	f.forceVerify = true
	f.scanning.Store(true)
	err := f.scan()
	f.scanning.Store(false)
	f.forceVerify = false
	return err
}

func (f *fileStore) categorySet(category string) (*categorySet, bool) {
	f.mu.RLock()
	set, ok := f.cache[category]
	f.mu.RUnlock()
	return set, ok
}

// Verify 校验文件是否仍在磁盘上；不存在则从缓存中移除，避免继续被抽到。
// 一次 stat 的成本在热 dentry 上约几微秒，远低于把用户 302 到 404 的代价。
func (f *fileStore) Verify(ctx context.Context, category, name string) bool {
	_ = ctx
	if name == "" || strings.ContainsAny(name, "/\\") || !f.validCategory(category) {
		return false
	}
	path := filepath.Join(f.dir, filepath.FromSlash(category), name)
	if _, err := os.Stat(path); err == nil {
		return true
	}
	f.removeFile(category, name)
	return false
}

// removeFile 从内存索引里摘掉一个文件（copy-on-write，读路径持有旧切片也安全）。
func (f *fileStore) removeFile(category, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	set, ok := f.cache[category]
	if !ok {
		return
	}
	filter := func(src []fileEntry) []fileEntry {
		out := make([]fileEntry, 0, len(src))
		for _, e := range src {
			if e.name != name {
				out = append(out, e)
			}
		}
		return out
	}
	set.all = filter(set.all)
	set.land = filter(set.land)
	set.port = filter(set.port)
}

// validCategory 用本实例的层级上限校验分类路径。
func (f *fileStore) validCategory(name string) bool {
	return IsValidCategoryDepth(name, f.maxDepth)
}

// aggFor 返回父目录聚合视图（仅当该路径是「只含子目录」的容器时才存在）。
func (f *fileStore) aggFor(category string) *aggKids {
	f.mu.RLock()
	agg := f.aggs[category]
	f.mu.RUnlock()
	return agg
}

func (f *fileStore) Categories(ctx context.Context) ([]string, error) {
	_ = ctx
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
	return f.CountWith(ctx, category, OrientationAny)
}

// CountWith 统计指定方向的文件数；方向桶为空时返回 0 而不是 ErrNotFound，
// 由调用方决定是否回退（服务端在无横图/竖图时会退回全集）。
func (f *fileStore) CountWith(ctx context.Context, category string, want Orientation) (int, error) {
	_ = ctx
	// 聚合优先：with_files 模式下父目录的聚合 = 自身图片 + 子目录。
	if agg := f.aggFor(category); agg != nil {
		return agg.bucket(want).total, nil
	}
	set, ok := f.categorySet(category)
	if !ok {
		return 0, ErrNotFound
	}
	return len(set.pick(want)), nil
}

// Random 随机抽取 limit 个文件名（不放回），不限方向。
func (f *fileStore) Random(ctx context.Context, category string, limit int) ([]Row, error) {
	return f.RandomWith(ctx, category, limit, OrientationAny)
}

// RandomWith 在指定方向的子集中随机抽取 limit 个（不放回）。
// 使用 rand/v2 全局 ChaCha8 随机源，自动按 goroutine 分片，无锁竞争；
// 只对前 limit 个位置做 Fisher-Yates 洗牌，分配与开销都是 O(limit)。
func (f *fileStore) RandomWith(ctx context.Context, category string, limit int, want Orientation) ([]Row, error) {
	_ = ctx
	// 聚合优先：父目录（含自身图片 + 子目录）走聚合桶。
	if agg := f.aggFor(category); agg != nil {
		rows := agg.bucket(want).randomRows(limit)
		if len(rows) == 0 {
			return nil, ErrNotFound
		}
		return rows, nil
	}
	set, ok := f.categorySet(category)
	if !ok {
		return nil, ErrNotFound
	}
	entries := set.pick(want)
	n := len(entries)
	if n == 0 {
		return nil, ErrNotFound
	}
	if limit > n {
		limit = n
	}
	if limit < 1 {
		limit = 1
	}
	// 稠密（抽取比例高）：下标副本 + 部分 Fisher-Yates，O(n) 分配但只需 limit 次交换。
	if limit*4 >= n {
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
			rows = append(rows, entries[i].row())
		}
		return rows, nil
	}
	// 稀疏（limit << n）：拒绝采样，分配 O(limit)，不动缓存切片。
	seen := make(map[int]struct{}, limit)
	rows := make([]Row, 0, limit)
	for len(rows) < limit {
		i := rand.IntN(n)
		if _, dup := seen[i]; dup {
			continue
		}
		seen[i] = struct{}{}
		rows = append(rows, entries[i].row())
	}
	return rows, nil
}

func (f *fileStore) ByIndex(ctx context.Context, category string, index int) (Row, error) {
	return f.ByIndexWith(ctx, category, index, OrientationAny)
}

// ByIndexWith 在指定方向的子集中按序号取图，序号从 1 起（文件名字典序）。
func (f *fileStore) ByIndexWith(ctx context.Context, category string, index int, want Orientation) (Row, error) {
	_ = ctx
	if index < 1 {
		return Row{}, ErrNotFound
	}
	if agg := f.aggFor(category); agg != nil {
		if row, ok := agg.bucket(want).rowAt(index - 1); ok {
			return row, nil
		}
		return Row{}, ErrNotFound
	}
	set, ok := f.categorySet(category)
	if !ok {
		return Row{}, ErrNotFound
	}
	entries := set.pick(want)
	if index > len(entries) {
		return Row{}, ErrNotFound
	}
	return entries[index-1].row(), nil
}

// LinksPage 返回 [offset, offset+limit) 区间的图片与总数。
// 大分类的后台预览只要 40 条，不必先复制上万条记录。
func (f *fileStore) LinksPage(ctx context.Context, category string, offset, limit int) ([]Row, int, error) {
	_ = ctx
	// 聚合优先：分页只按需取 rowAt，不会把整棵子树的条目materialize 出来。
	if agg := f.aggFor(category); agg != nil {
		b := &agg.all
		total := b.total
		if offset < 0 {
			offset = 0
		}
		if offset > total {
			offset = total
		}
		end := offset + limit
		if limit <= 0 || end > total {
			end = total
		}
		rows := make([]Row, 0, end-offset)
		for i := offset; i < end; i++ {
			if row, ok := b.rowAt(i); ok {
				rows = append(rows, row)
			}
		}
		return rows, total, nil
	}
	set, ok := f.categorySet(category)
	if !ok {
		return nil, 0, ErrNotFound
	}
	total := len(set.all)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if limit <= 0 || end > total {
		end = total
	}
	rows := make([]Row, 0, end-offset)
	for _, e := range set.all[offset:end] {
		rows = append(rows, e.row())
	}
	return rows, total, nil
}

func (f *fileStore) Links(ctx context.Context, category string) ([]Row, error) {
	_ = ctx
	if agg := f.aggFor(category); agg != nil {
		return agg.bucket(OrientationAny).allRows(), nil
	}
	set, ok := f.categorySet(category)
	if !ok {
		return nil, ErrNotFound
	}
	rows := make([]Row, 0, len(set.all))
	for _, e := range set.all {
		rows = append(rows, e.row())
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
	if !f.validCategory(name) {
		return errors.New("invalid category name")
	}
	path := filepath.Join(f.dir, filepath.FromSlash(name))
	if _, err := os.Stat(path); err == nil {
		return errors.New("分类已存在")
	}
	// 多级分类一次性建出全部上级目录。
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
	if !f.validCategory(category) {
		return "", errors.New("invalid category name")
	}
	filename = sanitizeFilename(filename)
	if !isImageFile(filename) {
		return "", fmt.Errorf("不支持的文件类型: %s", filename)
	}
	dir := filepath.Join(f.dir, filepath.FromSlash(category))
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
	if !f.validCategory(category) {
		return 0, errors.New("invalid category name")
	}
	dir := filepath.Join(f.dir, filepath.FromSlash(category))
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

// ScanWarnings 返回最近一次扫描中被跳过的目录及原因。
// 典型场景：目录名含 -/. / 空格、以数字开头、符号链接失效或回环。
func ScanWarnings(s Store) []string {
	fs, ok := s.(*fileStore)
	if !ok {
		return nil
	}
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	out := make([]string, len(fs.skipped))
	copy(out, fs.skipped)
	return out
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
	if !f.validCategory(name) {
		return errors.New("invalid category name")
	}
	path := filepath.Join(f.dir, filepath.FromSlash(name))
	if _, err := os.Stat(path); err != nil {
		return errors.New("分类不存在")
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	// 多级分类删除后，把因此空掉的上级目录一并清掉（删完 a/b 不留空壳 a）。
	f.pruneEmptyParents(name)
	return f.reload()
}

// pruneEmptyParents 自下而上删除空的上级目录，只处理数据根目录内的层级。
func (f *fileStore) pruneEmptyParents(category string) {
	segs := CategorySegments(category, f.maxDepth)
	for i := len(segs) - 1; i >= 1; i-- {
		parent := filepath.Join(f.dir, filepath.FromSlash(strings.Join(segs[:i], "/")))
		entries, err := os.ReadDir(parent)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(parent); err != nil {
			return
		}
	}
}
