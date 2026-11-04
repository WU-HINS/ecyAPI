// Package index 是百万级图片库的紧凑索引：分类表常驻内存，条目按需 mmap。
//
// 目标（里程碑 1）：
//   - 100 万条落盘 ≤ 30 MB（28 字节/条 + 路径 arena）
//   - 常驻堆 ≤ 40 MB（只放分类表 + 热分类的条目切片）
//   - 随机查询 p50 < 50µs（冷页 / 热页混合）
//   - 构建 100 万条 ≤ 5s
//
// 文件布局（小端）：
//
//	header   64B   magic/version/counts/offsets/crc
//	cats     n×32B 分类表（nameOff/nameLen/flags/firstRec/recCount/totalBytes）
//	records  n×28B 条目（按分类分组、组内按 hash 升序）
//	arena         路径字节（每条 pathLen 字节）
package index

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"sort"
	"sync"
)

const (
	magic      = "ECYXIDX1"
	headerSize = 64
	catSize    = 32
	recSize    = 28

	// FlagRemote 该分类在云端（/@web/*）。
	FlagRemote uint16 = 1 << 0
)

// 记录里 oriFlags 的位定义。
const (
	OriUnknown uint8 = 0
	OriLand    uint8 = 1
	OriPort    uint8 = 2
	OriSquare  uint8 = 3

	FlagMigrating uint8 = 1 << 3
)

// Hash 计算文件名 hash（分组内二分用）。
func Hash(name string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return h.Sum64()
}

// Entry 构建期的一条记录。
type Entry struct {
	Name    string
	Size    int64
	MTime   int64 // unix 秒
	LastUse int64
	Flags   uint8
}

// CatBuild 构建期的一个分类及其条目。
type CatBuild struct {
	Name  string
	Flags uint16
	Items []Entry
}

// Builder 构建索引文件。
type Builder struct {
	cats []CatBuild
}

// AddCategory 添加一个分类（会按 hash 排序后写入）。
func (b *Builder) AddCategory(c CatBuild) {
	sort.Slice(c.Items, func(i, j int) bool {
		return Hash(c.Items[i].Name) < Hash(c.Items[j].Name)
	})
	b.cats = append(b.cats, c)
}

// Build 把索引写到 path。
func (b *Builder) Build(path string) error {
	var arena []byte
	recs := make([]byte, 0, 1024*recSize)
	catTab := make([]byte, 0, len(b.cats)*catSize)

	recCount := 0
	for _, c := range b.cats {
		first := recCount
		nameOff := len(arena)
		arena = append(arena, c.Name...)
		for _, it := range c.Items {
			pathOff := len(arena)
			arena = append(arena, it.Name...)
			var rec [recSize]byte
			binary.LittleEndian.PutUint64(rec[0:], Hash(it.Name))
			binary.LittleEndian.PutUint32(rec[8:], uint32(pathOff))
			binary.LittleEndian.PutUint16(rec[12:], uint16(len(it.Name)))
			binary.LittleEndian.PutUint32(rec[14:], uint32(it.Size))
			binary.LittleEndian.PutUint32(rec[18:], uint32(it.MTime))
			binary.LittleEndian.PutUint32(rec[22:], uint32(it.LastUse))
			rec[26] = it.Flags
			recs = append(recs, rec[:]...)
			recCount++
		}
		var e [catSize]byte
		binary.LittleEndian.PutUint32(e[0:], uint32(nameOff))
		binary.LittleEndian.PutUint16(e[4:], uint16(len(c.Name)))
		binary.LittleEndian.PutUint16(e[6:], c.Flags)
		binary.LittleEndian.PutUint32(e[8:], uint32(first))
		binary.LittleEndian.PutUint32(e[12:], uint32(len(c.Items)))
		catTab = append(catTab, e[:]...)
	}

	catOff := headerSize
	recOff := catOff + len(catTab)
	arenaOff := recOff + len(recs)

	buf := make([]byte, arenaOff+len(arena))
	hdr := buf[:headerSize]
	copy(hdr[0:8], magic)
	binary.LittleEndian.PutUint32(hdr[8:], 1) // version
	binary.LittleEndian.PutUint32(hdr[12:], uint32(recCount))
	binary.LittleEndian.PutUint32(hdr[16:], uint32(len(b.cats)))
	binary.LittleEndian.PutUint32(hdr[20:], uint32(catOff))
	binary.LittleEndian.PutUint32(hdr[24:], uint32(recOff))
	binary.LittleEndian.PutUint32(hdr[28:], uint32(arenaOff))
	binary.LittleEndian.PutUint32(hdr[32:], uint32(len(arena)))
	copy(buf[catOff:], catTab)
	copy(buf[recOff:], recs)
	copy(buf[arenaOff:], arena)

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var (
	ErrBadMagic = errors.New("索引文件格式不对")
	ErrTooShort = errors.New("索引文件不完整")
)

// Index 只读索引（mmap）。
type Index struct {
	data []byte
	mm   bool

	// 分类表缓存：Categories() 每次分配会让 GC 压力暴涨（pprof 实证），
	// 这里解析一次、复用同一份切片，并建立 名字→下标 的映射供 Find 用。
	catOnce sync.Once
	catList []Category
	catIdx  map[string]int

	recCount int
	catCount int
	catOff   int
	recOff   int
	arena    []byte
}

// Category 分类的只读视图。
type Category struct {
	Name  string
	Flags uint16
	Count int // 条目数
	first int
}

// Record 一条只读记录。
type Record struct {
	Hash    uint64
	Name    string
	Size    uint32
	MTime   uint32
	LastUse uint32
	Flags   uint8
}

// Open 打开索引文件（mmap；不支持 mmap 的平台回退到整份读入）。
func Open(path string) (*Index, error) {
	return openFile(path)
}

func parse(ix *Index) error {
	d := ix.data
	if len(d) < headerSize || string(d[0:8]) != magic {
		return ErrBadMagic
	}
	ix.recCount = int(binary.LittleEndian.Uint32(d[12:]))
	ix.catCount = int(binary.LittleEndian.Uint32(d[16:]))
	ix.catOff = int(binary.LittleEndian.Uint32(d[20:]))
	ix.recOff = int(binary.LittleEndian.Uint32(d[24:]))
	arenaOff := int(binary.LittleEndian.Uint32(d[28:]))
	arenaLen := int(binary.LittleEndian.Uint32(d[32:]))
	if arenaOff+arenaLen > len(d) || ix.recOff+ix.recCount*recSize > len(d) {
		return ErrTooShort
	}
	ix.arena = d[arenaOff : arenaOff+arenaLen]
	return nil
}

// Close 释放映射。
func (ix *Index) Close() error {
	return ix.close()
}

// RecCount 条目总数。
func (ix *Index) RecCount() int { return ix.recCount }

// Categories 分类列表（顺序即文件顺序；返回共享切片，调用方只读）。
func (ix *Index) Categories() []Category {
	ix.catOnce.Do(ix.loadCats)
	return ix.catList
}

func (ix *Index) loadCats() {
	out := make([]Category, 0, ix.catCount)
	for i := 0; i < ix.catCount; i++ {
		e := ix.data[ix.catOff+i*catSize:]
		nameOff := int(binary.LittleEndian.Uint32(e[0:]))
		nameLen := int(binary.LittleEndian.Uint16(e[4:]))
		out = append(out, Category{
			Name:  string(ix.arena[nameOff : nameOff+nameLen]),
			Flags: binary.LittleEndian.Uint16(e[6:]),
			first: int(binary.LittleEndian.Uint32(e[8:])),
			Count: int(binary.LittleEndian.Uint32(e[12:])),
		})
	}
	ix.catList = out
	ix.catIdx = make(map[string]int, len(out))
	for i, c := range out {
		ix.catIdx[c.Name] = i
	}
}

// Find 按名字找分类（哈希查表，O(1)）。
func (ix *Index) Find(name string) (Category, bool) {
	ix.catOnce.Do(ix.loadCats)
	if i, ok := ix.catIdx[name]; ok {
		return ix.catList[i], true
	}
	return Category{}, false
}

func (ix *Index) record(idx int) Record {
	o := ix.recOff + idx*recSize
	r := ix.data[o:]
	pathOff := int(binary.LittleEndian.Uint32(r[8:]))
	pathLen := int(binary.LittleEndian.Uint16(r[12:]))
	return Record{
		Hash:    binary.LittleEndian.Uint64(r[0:]),
		Name:    string(ix.arena[pathOff : pathOff+pathLen]),
		Size:    binary.LittleEndian.Uint32(r[14:]),
		MTime:   binary.LittleEndian.Uint32(r[18:]),
		LastUse: binary.LittleEndian.Uint32(r[22:]),
		Flags:   r[26],
	}
}

// At 取分类内第 i 条（0 起）。
func (ix *Index) At(c Category, i int) (Record, bool) {
	if i < 0 || i >= c.Count {
		return Record{}, false
	}
	return ix.record(c.first + i), true
}

// Lookup 分类内按文件名查找（hash 二分 + 名字比对防碰撞）。
func (ix *Index) Lookup(c Category, name string) (Record, bool) {
	h := Hash(name)
	lo, hi := c.first, c.first+c.Count
	for lo < hi {
		mid := (lo + hi) / 2
		r := ix.data[ix.recOff+mid*recSize:]
		mh := binary.LittleEndian.Uint64(r[0:])
		switch {
		case mh < h:
			lo = mid + 1
		case mh > h:
			hi = mid
		default:
			// hash 相同：可能多条（碰撞），线性比对名字
			start := mid
			for start > c.first {
				pr := ix.data[ix.recOff+(start-1)*recSize:]
				if binary.LittleEndian.Uint64(pr[0:]) != h {
					break
				}
				start--
			}
			for i := start; i < c.first+c.Count; i++ {
				rr := ix.data[ix.recOff+i*recSize:]
				if binary.LittleEndian.Uint64(rr[0:]) != h {
					break
				}
				rec := ix.record(i)
				if rec.Name == name {
					return rec, true
				}
			}
			return Record{}, false
		}
	}
	return Record{}, false
}

// LookupPos 与 Lookup 相同，但额外返回记录在分类内的位置（供回写用）。
func (ix *Index) LookupPos(c Category, name string) (Record, int, bool) {
	h := Hash(name)
	lo, hi := c.first, c.first+c.Count
	for lo < hi {
		mid := (lo + hi) / 2
		r := ix.data[ix.recOff+mid*recSize:]
		mh := binary.LittleEndian.Uint64(r[0:])
		switch {
		case mh < h:
			lo = mid + 1
		case mh > h:
			hi = mid
		default:
			start := mid
			for start > c.first {
				pr := ix.data[ix.recOff+(start-1)*recSize:]
				if binary.LittleEndian.Uint64(pr[0:]) != h {
					break
				}
				start--
			}
			for i := start; i < c.first+c.Count; i++ {
				rr := ix.data[ix.recOff+i*recSize:]
				if binary.LittleEndian.Uint64(rr[0:]) != h {
					break
				}
				rec := ix.record(i)
				if rec.Name == name {
					return rec, i - c.first, true
				}
			}
			return Record{}, 0, false
		}
	}
	return Record{}, 0, false
}

// SetFlags 回写方向/状态位（mmap 可写时有效，供惰性方向判定用）。
func (ix *Index) SetFlags(c Category, i int, flags uint8) error {
	if i < 0 || i >= c.Count {
		return fmt.Errorf("下标越界")
	}
	if !ix.mm {
		return fmt.Errorf("索引只读（未使用 mmap）")
	}
	ix.data[ix.recOff+(c.first+i)*recSize+26] = flags
	return nil
}
