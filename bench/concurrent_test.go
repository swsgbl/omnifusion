// 并发连接冒烟压测（ 指标 3）：N 个 goroutine 各发 3 个内容互异
// 的非流式请求（互异防语义缓存命中），统计 wall time、成功率、p50/p99
// 延迟与失败状态码分布。默认 200 并发快速版保证 `go test ./...` 不超时；
// OFD_BENCH_HEAVY=1 时升级 1000 并发全量口径。断言成功率 ≥99%（冒烟
// 门槛而非硬性能门槛）。
package bench_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// concurrentWorkers 决定并发规模：环境开关 OFD_BENCH_HEAVY=1 时 1000。
func concurrentWorkers() int {
	if os.Getenv("OFD_BENCH_HEAVY") == "1" {
		return 1000
	}
	return 200
}

// Test1000ConcurrentConnections 冒烟级并发压测。
func Test1000ConcurrentConnections(t *testing.T) {
	skipIfUnavailable(t)
	if testing.Short() {
		t.Skip("-short 模式跳过并发压测")
	}
	workers := concurrentWorkers()
	const reqs = 3
	// 重载模式下排队尾部等待更久（store 单连接串行写），放宽单请求超时。
	reqTimeout := 60 * time.Second
	if workers >= 500 {
		reqTimeout = 300 * time.Second
	}
	codes := make([]int, workers*reqs) // 0=传输层错误，其余为 HTTP 状态码
	latencies := make([]time.Duration, workers*reqs)

	// 建连斜坡（总宽 2s）：Windows 上 1000 个瞬时 SYN 会溢出 accept 背压被
	// RST——裸 net/http 对照实验同样 ~77% 被拒，属平台行为而非网关缺陷
	// （Linux 丢 SYN 重传无此现象）。错峰握手后全部连接仍并发活跃，
	// 测的才是「N 并发连接」本身而非连接风暴。
	rampStep := 2 * time.Second / time.Duration(workers)
	var wg sync.WaitGroup
	startWall := time.Now()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			time.Sleep(time.Duration(w) * rampStep)
			for j := 0; j < reqs; j++ {
				body := chatBody(fmt.Sprintf("并发压测 worker=%d req=%d 唯一内容", w, j), false)
				t0 := time.Now()
				codes[w*reqs+j] = fireOnce(body, reqTimeout)
				latencies[w*reqs+j] = time.Since(t0)
			}
		}(w)
	}
	wg.Wait()
	wall := time.Since(startWall)

	logConcurrentReport(t, workers, reqs, codes, latencies, wall)
	if success := countSuccess(codes); float64(success)/float64(workers*reqs) < 0.99 {
		t.Errorf("成功率 %.2f%% 低于 99%% 冒烟门槛", 100*float64(success)/float64(workers*reqs))
	}
}

// fireOnce 发单个带超时上限的非流式请求，返回 HTTP 状态码（传输层错误
// 记 0），失败样例进 failSamples 供报告展示。
func fireOnce(body []byte, timeout time.Duration) int {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	resp, err := doChatCtx(ctx, body)
	if err != nil {
		recordFailure(0, err.Error())
		return 0
	}
	code := resp.StatusCode
	if code != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		recordFailure(code, string(snippet))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return code
}

// failSamples 保留最近的失败样例（状态码+响应体片段），容量有界。
var (
	failMu      sync.Mutex
	failSamples []string
)

// recordFailure 追加一条失败样例（最多保留 8 条）。
func recordFailure(code int, detail string) {
	failMu.Lock()
	defer failMu.Unlock()
	failSamples = append(failSamples, fmt.Sprintf("status=%d %s", code, detail))
	if len(failSamples) > 8 {
		failSamples = failSamples[len(failSamples)-8:]
	}
}

// countSuccess 统计 200 响应数。
func countSuccess(codes []int) int {
	n := 0
	for _, c := range codes {
		if c == http.StatusOK {
			n++
		}
	}
	return n
}

// logConcurrentReport 汇总 wall time、吞吐、状态码分布、p50/p99/max
// 延迟与失败样例，并采样网关内存。
func logConcurrentReport(t *testing.T, workers, reqs int, codes []int, latencies []time.Duration, wall time.Duration) {
	total := workers * reqs
	dist := make(map[int]int)
	okLat := make([]time.Duration, 0, total)
	for i, c := range codes {
		dist[c]++
		if c == http.StatusOK {
			okLat = append(okLat, latencies[i])
		}
	}
	sort.Slice(okLat, func(i, j int) bool { return okLat[i] < okLat[j] })
	t.Logf("并发连接=%d 每连接请求=%d 总数=%d 成功=%d 失败=%d",
		workers, reqs, total, countSuccess(codes), total-countSuccess(codes))
	t.Logf("wall=%v 吞吐=%.1f req/s 状态码分布=%v",
		wall.Round(time.Millisecond), float64(total)/wall.Seconds(), dist)
	if len(okLat) > 0 {
		t.Logf("延迟（成功样本 n=%d）：p50=%v p99=%v max=%v",
			len(okLat), percentile(okLat, 50), percentile(okLat, 99), okLat[len(okLat)-1])
	}
	failMu.Lock()
	samples := append([]string(nil), failSamples...)
	failMu.Unlock()
	for _, s := range samples {
		t.Logf("失败样例：%s", strings.TrimSpace(s))
	}
	if len(samples) > 0 {
		logFailureContext(t)
	}
	sampleGatewayRSS(t)
}

// logFailureContext 失败发生时的网关现场：进程是否存活、监听口状态、
// 网关日志尾部（定位崩溃/关停/accept 异常）。
func logFailureContext(t *testing.T) {
	t.Helper()
	select {
	case <-gwExited:
		t.Log("失败现场：网关子进程已退出（疑似崩溃/被终止）")
	default:
		if portHasListener(gatewayAddr) {
			t.Log("失败现场：网关子进程存活且仍在监听（疑似监听背压/限流）")
		} else {
			t.Log("失败现场：网关子进程存活但监听口不再接受连接")
		}
	}
	if gwLogPath != "" {
		t.Logf("网关日志尾部：%s", tailFile(gwLogPath, 2048))
	}
}

// percentile 求已排序延迟样本的 p 分位（就近取下标）。
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[int(float64(len(sorted)-1)*p/100)]
}

// sampleGatewayRSS 采样网关子进程常驻内存：仅 Linux 可从 /proc 直读；
// Windows 无等价物，跳过并说明（本基线表采集环境即 Windows）。
func sampleGatewayRSS(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" || gwPID == 0 {
		t.Logf("网关内存采样跳过（%s 无 /proc 等价物，内存列见 README 说明）", runtime.GOOS)
		return
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", gwPID))
	if err != nil {
		t.Logf("读 /proc/%d/status 失败：%v", gwPID, err)
		return
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			t.Logf("网关子进程内存：%s", strings.TrimSpace(line))
			return
		}
	}
}
