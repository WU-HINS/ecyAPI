package server

import (
	"os"
	"time"
)

// previewJob 一个后台补缓存任务。
type previewJob struct {
	path      string // 源图
	cachePath string // 缓存落盘路径
	width     int
	units     int // 占用的像素预算
}

// enqueuePreviewJob 把补缓存任务丢进队列（非阻塞）。
// 队列满时直接丢弃：补缓存是尽力而为，绝不能反过来把请求线程拖住。
func (s *Server) enqueuePreviewJob(job previewJob) {
	select {
	case s.previewJobs <- job:
	default:
	}
}

// previewWorker 后台串行补缓存。
//
// 为什么需要它：冷缓存时若预算已满就直接回退原图，会导致「图库持续预览」
// 永远填不满缓存——每次浏览都在重复解码。这里改成：请求立刻回退原图（快），
// 同时排一个后台任务在预算空出来时补上缓存，下次访问就是命中。
func (s *Server) previewWorker() {
	for job := range s.previewJobs {
		if fi, err := os.Stat(job.cachePath); err == nil && fi.Size() > 0 {
			continue // 这期间已被别的请求填好
		}
		if !s.previewGate.acquireWait(job.units, 5*time.Second) {
			continue // 预算一直紧张：放弃这次补缓存
		}
		data, _, err := compressPreview(job.path, job.width)
		s.previewGate.release(job.units)
		if err != nil || len(data) == 0 {
			continue
		}
		if err := s.writePreviewFile(job.cachePath, data, job.path); err != nil {
			continue
		}
		s.evictPreviewCache()
	}
}
