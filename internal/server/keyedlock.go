package server

import "sync"

// keyedLocks 为一组字符串键提供互斥（带引用计数，键用完即回收）。
// 用于把「同一张图的同宽度压缩」串行化：并发请求只有一个真正干活，
// 其余等它落盘后直接命中缓存，避免重复解码与重复编码。
type keyedLocks struct {
	mu sync.Mutex
	m  map[string]*keyedLock
}

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

func newKeyedLocks() *keyedLocks {
	return &keyedLocks{m: map[string]*keyedLock{}}
}

// lock 获取键锁，返回解锁函数（必须调用）。
func (k *keyedLocks) lock(key string) func() {
	k.mu.Lock()
	kl := k.m[key]
	if kl == nil {
		kl = &keyedLock{}
		k.m[key] = kl
	}
	kl.refs++
	k.mu.Unlock()

	kl.mu.Lock()
	return func() {
		kl.mu.Unlock()
		k.mu.Lock()
		kl.refs--
		if kl.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}
