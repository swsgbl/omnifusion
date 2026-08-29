// 非流式 chat 基准：稳态缓存命中（BenchmarkChatNonStream）与强制 miss
// （BenchmarkChatCacheMiss）对照，二者 ns/op 之差即 docs/05 要求的
// 「缓存命中延迟」。本文件同时存放各基准共用的请求助手。
package bench_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// missSeq 进程级唯一序号：基准框架会以 b.N=1 试跑后再放大 N、-count 也会
// 重复整个函数，循环下标会回卷导致同内容二打命中缓存，故必须全局唯一。
var missSeq atomic.Int64

// chatBody 构造 chat 请求体（stream 可控），user 内容由调用方注入以
// 控制缓存命中语义。
func chatBody(content string, stream bool) []byte {
	streamFlag := "false"
	if stream {
		streamFlag = "true"
	}
	return fmt.Appendf(nil, `{"model":%q,"stream":%s,"messages":[`+
		`{"role":"system","content":"你是基准测试助手"},`+
		`{"role":"user","content":%q}]}`, benchModel, streamFlag, content)
}

// doChatCtx 发一个 chat 请求（带 ctx）；调用方负责读尽 body 以复用连接。
func doChatCtx(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		gatewayURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+gwAPIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Session-Id", benchSession) // sticky 直达 mock 上游
	return benchClient.Do(req)
}

// doChat 是 doChatCtx 的无 ctx 便捷封装。
func doChat(body []byte) (*http.Response, error) {
	return doChatCtx(context.Background(), body)
}

// drainResp 读尽并关闭响应体（保 keep-alive）。
func drainResp(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// warmUntilCacheHit 以同一请求体反复请求直到 X-OmniFusion-Cache: hit：
// 缓存回写是上游成功后异步进行的，只预热一次可能在回写落地前再打 miss。
func warmUntilCacheHit(tb testing.TB, body []byte) {
	tb.Helper()
	for i := 0; i < 100; i++ {
		resp, err := doChat(body)
		if err != nil {
			tb.Fatalf("预热请求失败: %v", err)
		}
		hit := resp.Header.Get("X-OmniFusion-Cache")
		drainResp(resp)
		if hit == "hit" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	tb.Fatal("预热 100 次仍未命中语义缓存")
}

// BenchmarkChatNonStream 测含语义缓存的稳态延迟：同一请求体 b.N 次，
// 预热到 hit 后 ResetTimer，全部走缓存命中路径（TTFT 目标 <10ms 的口径）。
func BenchmarkChatNonStream(b *testing.B) {
	skipIfUnavailable(b)
	body := chatBody("固定请求：缓存命中稳态基准", false)
	warmUntilCacheHit(b, body)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := doChat(body)
		if err != nil {
			b.Fatalf("请求失败: %v", err)
		}
		cache := resp.Header.Get("X-OmniFusion-Cache")
		if resp.StatusCode != http.StatusOK || cache != "hit" {
			b.Fatalf("预期缓存命中：status=%d cache=%q", resp.StatusCode, cache)
		}
		drainResp(resp)
	}
}

// BenchmarkChatCacheMiss 每次迭代用唯一 user 内容强制 miss，走完整链路
// （鉴权→缓存查询未命中→路由→上游→审计→异步回写）。
func BenchmarkChatCacheMiss(b *testing.B) {
	skipIfUnavailable(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body := chatBody(fmt.Sprintf("唯一内容 seq=%d：强制缓存 miss", missSeq.Add(1)), false)
		resp, err := doChat(body)
		if err != nil {
			b.Fatalf("请求失败: %v", err)
		}
		cache := resp.Header.Get("X-OmniFusion-Cache")
		if resp.StatusCode != http.StatusOK || cache != "miss" {
			b.Fatalf("预期缓存 miss：status=%d cache=%q", resp.StatusCode, cache)
		}
		drainResp(resp)
	}
}
