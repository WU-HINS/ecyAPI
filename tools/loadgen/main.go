// loadgen 是一个极简 HTTP 压测客户端，用于复现 docs/benchmarks.md 里的数字。
//
//	go run ./tools/loadgen <url> [并发数] [请求数 | 时长]
//	go run ./tools/loadgen 'http://127.0.0.1:8080/@img' 100 8s -json
//
// 第三参数写成 "8s" 表示按时长压测（CI 用这种），写数字表示总请求数。
// -json 输出机器可读结果，供压测工作流拼报告。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
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

type result struct {
	URL       string  `json:"url"`
	Workers   int     `json:"workers"`
	DurationS float64 `json:"duration_s"`
	Requests  int64   `json:"requests"`
	QPS       float64 `json:"qps"`
	P50ms     float64 `json:"p50_ms"`
	P99ms     float64 `json:"p99_ms"`
	Failed    int64   `json:"failed"`
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法: loadgen <url> [并发数] [请求数|时长如 8s] [-json]")
		os.Exit(2)
	}
	url := os.Args[1]
	workers := argInt(2, 100)
	mode := ""
	if len(os.Args) > 3 {
		mode = os.Args[3]
	}
	jsonOut := false
	for _, a := range os.Args[2:] {
		if a == "-json" {
			jsonOut = true
		}
	}

	total := 0
	var dur time.Duration
	if strings.HasSuffix(mode, "s") {
		if d, err := time.ParseDuration(mode); err == nil && d > 0 {
			dur = d
		}
	}
	if dur == 0 {
		total = argInt(3, 20000)
		if total <= 0 {
			total = 20000
		}
	}

	var wg sync.WaitGroup
	var done, fail int64
	var mu sync.Mutex
	lat := make([]time.Duration, 0, 200000)
	tr := &http.Transport{MaxIdleConnsPerHost: 512, MaxConnsPerHost: 512}
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	if os.Getenv("LG_NOREDIRECT") == "1" {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	if os.Getenv("LG_GZIP") == "1" {
		base := tr
		client.Transport = rtFunc(func(req *http.Request) (*http.Response, error) {
			req.Header.Set("Accept-Encoding", "gzip")
			return base.RoundTrip(req)
		})
	}

	start := time.Now()
	deadline := time.Time{}
	if dur > 0 {
		deadline = start.Add(dur)
	}
	job := make(chan int, 4096)
	go func() {
		if dur > 0 {
			for time.Now().Before(deadline) {
				job <- 0
			}
			close(job)
			return
		}
		for i := 0; i < total; i++ {
			job <- 0
		}
		close(job)
	}()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range job {
				if dur > 0 && time.Now().After(deadline) {
					continue
				}
				t0 := time.Now()
				resp, err := client.Get(url)
				if err != nil {
					atomic.AddInt64(&fail, 1)
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode >= 400 {
					atomic.AddInt64(&fail, 1)
				}
				d := time.Since(t0)
				mu.Lock()
				lat = append(lat, d)
				mu.Unlock()
				atomic.AddInt64(&done, 1)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pct := func(q float64) time.Duration {
		if len(lat) == 0 {
			return 0
		}
		return lat[int(float64(len(lat)-1)*q)]
	}
	reqs := atomic.LoadInt64(&done)
	res := result{
		URL: url, Workers: workers, DurationS: elapsed.Seconds(),
		Requests: reqs, QPS: float64(reqs) / elapsed.Seconds(),
		P50ms:  float64(pct(0.5).Microseconds()) / 1000,
		P99ms:  float64(pct(0.99).Microseconds()) / 1000,
		Failed: atomic.LoadInt64(&fail),
	}
	if jsonOut {
		b, _ := json.Marshal(res)
		fmt.Println(string(b))
		return
	}
	fmt.Printf("并发%d 时长%.0fs 请求%d QPS≈%.0f p50=%v p99=%v 失败%d\n",
		workers, elapsed.Seconds(), reqs, res.QPS,
		pct(0.5).Round(100*time.Microsecond), pct(0.99).Round(100*time.Microsecond), res.Failed)
}
