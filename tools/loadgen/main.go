// loadgen 是一个极简 HTTP 压测客户端，用于复现 docs/benchmarks.md 里的数字。
//
//	go run ./tools/loadgen <url> [并发数] [总请求数]
//	go run ./tools/loadgen 'http://127.0.0.1:8080/json?pc' 100 20000
//
// 注意：与服务端跑在同一台机器上时，客户端本身会吃掉一部分 CPU，
// 高并发下的 QPS 是「客户端 + 服务端」的合计上限，不是服务端单机极限。
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

func argInt(i, def int) int {
	if len(os.Args) > i {
		if v, err := strconv.Atoi(os.Args[i]); err == nil {
			return v
		}
	}
	return def
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法: loadgen <url> [并发数] [总请求数]")
		os.Exit(2)
	}
	url := os.Args[1]
	workers := argInt(2, 100)
	total := argInt(3, 20000)

	jobs := make(chan int, total)
	var wg sync.WaitGroup
	var fail int64
	lat := make([]time.Duration, 0, total)
	var mu sync.Mutex

	tr := &http.Transport{MaxIdleConnsPerHost: 512, MaxConnsPerHost: 512}
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}

	start := time.Now()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				t0 := time.Now()
				resp, err := client.Get(url)
				if err != nil {
					atomic.AddInt64(&fail, 1)
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					atomic.AddInt64(&fail, 1)
				}
				d := time.Since(t0)
				mu.Lock()
				lat = append(lat, d)
				mu.Unlock()
			}
		}()
	}
	for i := 0; i < total; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	elapsed := time.Since(start)

	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pct := func(q float64) time.Duration { return lat[int(float64(len(lat)-1)*q)] }
	fmt.Printf("并发%d 请求%d 耗时%.2fs QPS≈%.0f p50=%v p99=%v 失败=%d\n",
		workers, total, elapsed.Seconds(), float64(total)/elapsed.Seconds(), pct(0.5), pct(0.99), fail)
}
