package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

var (
	concurrency = flag.Int("c", 10, "并发数（goroutine 数量）")
	requests    = flag.Int("n", 100, "每个并发的请求次数")
	url         = flag.String("url", "", "目标URL")
	token       = flag.String("token", "", "Bearer Token（可选）")
)

type RequestResult struct {
	duration time.Duration
	err      string
}

type ByDuration []time.Duration

func (a ByDuration) Len() int           { return len(a) }
func (a ByDuration) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByDuration) Less(i, j int) bool { return a[i] < a[j] }

func main() {
	flag.Parse()

	if *url == "" {
		fmt.Println("请使用 -url 指定目标地址")
		return
	}

	totalRequests := *concurrency * *requests

	var wg sync.WaitGroup
	var success, failed int
	statsCode := make(map[int]int) // 状态码统计
	stats := make(map[string]int)  // 状态统计
	var mu sync.Mutex
	var results []RequestResult

	start := time.Now()

	tr := &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 200, // >= concurrency
		IdleConnTimeout:     90 * time.Second,
	}
	client := &http.Client{
		Transport: tr,
		Timeout:   10 * time.Second,
	}

	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for j := 0; j < *requests; j++ {
				reqstart := time.Now()

				req, err := http.NewRequest("GET", *url, nil)
				if err != nil {
					mu.Lock()
					failed++
					results = append(results, RequestResult{duration: time.Since(reqstart), err: err.Error()})
					mu.Unlock()
					continue
				}

				if *token != "" {
					req.Header.Set("Authorization", "Bearer "+*token)
				}

				resp, err := client.Do(req)
				if err != nil {
					// ⚠️ err != nil 时 resp 可能为 nil，不能访问 resp.StatusCode
					mu.Lock()
					results = append(results, RequestResult{duration: time.Since(reqstart), err: err.Error()})
					failed++
					stats[err.Error()]++ // 表示连接失败/超时等无状态码的情况
					mu.Unlock()
					continue
				}

				// 读完 Body 再关闭（好习惯，复用连接）
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()

				reqend := time.Since(reqstart)

				mu.Lock()
				statsCode[resp.StatusCode]++ // ✅ 成功/非200都统计进来
				results = append(results, RequestResult{duration: reqend, err: ""})
				if resp.StatusCode == 200 {
					success++
				} else {
					failed++
				}
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	elapsed := time.Since(start)

	var durations []time.Duration
	for _, r := range results {
		if r.err == "" {
			durations = append(durations, r.duration)
		}
	}
	sort.Sort(ByDuration(durations))
	n := len(durations)

	fmt.Printf("并发数: %d\n", *concurrency)
	fmt.Printf("每并发请求: %d 次\n", *requests)
	fmt.Printf("总请求数: %d\n", totalRequests)
	fmt.Printf("总耗时: %v\n", elapsed)
	fmt.Println("========================================")
	fmt.Printf("成功: %d, 失败: %d\n", success, failed)
	fmt.Printf("Successs QPS: %.2f\n", float32(success)/float32(elapsed.Seconds()))
	fmt.Printf("P50: %v\n", durations[int(float64(n)*0.5)])
	fmt.Printf("P95: %v\n", durations[int(float64(n)*0.95)])
	fmt.Printf("P99: %v\n", durations[int(float64(n)*0.99)])
	fmt.Println("\n状态码分布:")
	for code, count := range statsCode {
		fmt.Printf("  HTTP %d: %d 次\n", code, count)
	}
	for sts, count := range stats {
		fmt.Printf("  HTTP %s: %d 次\n", sts, count)
	}
}
