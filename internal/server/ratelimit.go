package server

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 后台登录限流参数：同一 IP 在 window 内失败 maxLoginFails 次，锁定 lockout。
// 目的是把「默认口令 + 无限次尝试」这条最廉价的攻击路径堵掉。
const (
	maxLoginFails = 8
	loginWindow   = 5 * time.Minute
	loginLockout  = 5 * time.Minute
)

// loginLimiter 按来源 IP 统计登录失败次数，带锁定与过期清理。
type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]*loginAttempt
	now     func() time.Time
}

type loginAttempt struct {
	fails int
	last  time.Time
	until time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{entries: map[string]*loginAttempt{}, now: time.Now}
}

// blocked 判断该 IP 是否处于锁定中。
func (l *loginLimiter) blocked(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[ip]
	if e == nil {
		return false, 0
	}
	now := l.now()
	if now.Before(e.until) {
		return true, e.until.Sub(now)
	}
	return false, 0
}

// fail 记录一次失败；达到阈值后进入锁定期。
func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e := l.entries[ip]
	if e == nil || now.Sub(e.last) > loginWindow {
		e = &loginAttempt{}
		l.entries[ip] = e
	}
	e.fails++
	e.last = now
	if e.fails >= maxLoginFails {
		e.until = now.Add(loginLockout)
		e.fails = 0
	}
	l.pruneLocked(now)
}

// reset 登录成功后清除该 IP 的失败记录。
func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	delete(l.entries, ip)
	l.mu.Unlock()
}

// pruneLocked 清理长期未活动的记录，避免被大量来源 IP 撑大内存。
// 调用方必须已持有 l.mu。
func (l *loginLimiter) pruneLocked(now time.Time) {
	if len(l.entries) < 4096 {
		return
	}
	for ip, e := range l.entries {
		if now.Sub(e.last) > loginWindow && now.After(e.until) {
			delete(l.entries, ip)
		}
	}
}

// clientIP 取调用方 IP。只信任连接来源，不解析 X-Forwarded-For：
// 该头可被客户端伪造，用它做限流等于把锁定开关交给攻击者。
// 若部署在 CDN 后面，应由反代层做登录入口保护。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}
