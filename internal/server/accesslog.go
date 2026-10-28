package server

import (
	"bufio"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// accessLog 异步访问日志：请求路径只做一次非阻塞投递，
// 避免标准库 log 的全局互斥锁成为高并发下的热点。
// 队列满时直接丢弃（访问日志不应阻塞业务），丢弃数计入 drop。
type accessLog struct {
	ch   chan logEntry
	drop atomic.Uint64
}

type logEntry struct {
	method string
	path   string
	dur    time.Duration
}

// accessLogEnabled 访问日志开关：ECY_ACCESS_LOG=off/0/false 时关闭，
// 压测与高频场景可关掉以省去全部格式化开销。
func accessLogEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ECY_ACCESS_LOG"))) {
	case "off", "0", "false", "no":
		return false
	}
	return true
}

func newAccessLog() *accessLog {
	l := &accessLog{ch: make(chan logEntry, 8192)}
	var w io.Writer = io.Discard
	if accessLogEnabled() {
		w = os.Stdout
	}
	go l.run(w)
	return l
}

// add 投递一条日志，永不阻塞。
func (l *accessLog) add(method, path string, dur time.Duration) {
	select {
	case l.ch <- logEntry{method: method, path: path, dur: dur}:
	default:
		l.drop.Add(1)
	}
}

// run 由单个 goroutine 串行格式化并写盘，队列排空时立即 Flush。
func (l *accessLog) run(out io.Writer) {
	w := bufio.NewWriterSize(out, 32<<10)
	for e := range l.ch {
		_, _ = w.WriteString(time.Now().Format("2006/01/02 15:04:05.000 "))
		_, _ = w.WriteString(e.method)
		_ = w.WriteByte(' ')
		_, _ = w.WriteString(e.path)
		_ = w.WriteByte(' ')
		_, _ = w.WriteString(e.dur.Round(time.Microsecond).String())
		_ = w.WriteByte('\n')
		if len(l.ch) == 0 {
			_ = w.Flush()
		}
	}
}
