// 流式基准（docs/05 指标 1）：TTFT —— 从发请求到读到首个 data: 行的
// 耗时（ttft-ns/op 自定义指标）；整流 —— 完整 SSE 到 [DONE] 的端到端
// 耗时（ns/op，含 mock 上游 3×2ms chunk 间隔，口径对齐 scripts/mockup）。
package bench_test

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// doStreamChat 发起流式请求，返回响应与包了 body 的行 reader。
func doStreamChat(body []byte) (*http.Response, *bufio.Reader, error) {
	resp, err := doChat(body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		drainResp(resp)
		return nil, nil, fmt.Errorf("流式请求状态 %d", resp.StatusCode)
	}
	return resp, bufio.NewReader(resp.Body), nil
}

// readFirstDataLine 读到首个以 data: 开头的行即返回（TTFT 停表点）。
func readFirstDataLine(r *bufio.Reader) error {
	for {
		line, err := r.ReadString('\n')
		if strings.HasPrefix(strings.TrimSpace(line), "data:") {
			return nil
		}
		if err != nil {
			return fmt.Errorf("未读到 data: 行：%w", err)
		}
	}
}

// BenchmarkStreamTTFT 测首 chunk 到达耗时：循环内单次流式请求，从发请求
// 计时到首个 data: 行，累计后以 ttft-ns/op 报告均值；剩余 chunk 读完
// 弃置以保证连接复用，其耗时只计入 ns/op 不污染 ttft 口径。
func BenchmarkStreamTTFT(b *testing.B) {
	skipIfUnavailable(b)
	body := chatBody("流式 TTFT 基准", true)
	var total time.Duration
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		resp, reader, err := doStreamChat(body)
		if err != nil {
			b.Fatalf("请求失败: %v", err)
		}
		if readErr := readFirstDataLine(reader); readErr != nil {
			b.Fatal(readErr)
		}
		total += time.Since(start)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	b.StopTimer()
	b.ReportMetric(float64(total)/float64(b.N), "ttft-ns/op")
}

// BenchmarkStreamFull 测完整 SSE 端到端耗时：读尽响应体并校验以
// [DONE] 收尾。
func BenchmarkStreamFull(b *testing.B) {
	skipIfUnavailable(b)
	body := chatBody("流式整流基准", true)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, _, err := doStreamChat(body)
		if err != nil {
			b.Fatalf("请求失败: %v", err)
		}
		all, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			b.Fatalf("读流失败: %v", readErr)
		}
		if !strings.Contains(string(all), "[DONE]") {
			b.Fatalf("流未以 [DONE] 收尾：%.200s", all)
		}
	}
}
