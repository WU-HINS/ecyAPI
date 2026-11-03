//go:build linux || darwin

package index

import (
	"os"
	"syscall"
)

// openFile 用 mmap 打开索引（读写映射，便于回写方向位）。
func openFile(path string) (*Index, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() == 0 {
		return nil, ErrTooShort
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(fi.Size()), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	ix := &Index{data: data, mm: true}
	if err := parse(ix); err != nil {
		_ = syscall.Munmap(data)
		return nil, err
	}
	return ix, nil
}

func (ix *Index) close() error {
	if ix.mm && ix.data != nil {
		return syscall.Munmap(ix.data)
	}
	return nil
}
