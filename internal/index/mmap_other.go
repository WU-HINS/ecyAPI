//go:build !linux && !darwin

package index

import "os"

// openFile 回退实现：整份读入内存（不支持 mmap 的平台）。
func openFile(path string) (*Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	ix := &Index{data: data}
	if err := parse(ix); err != nil {
		return nil, err
	}
	return ix, nil
}

func (ix *Index) close() error { return nil }
