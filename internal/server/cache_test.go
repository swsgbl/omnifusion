package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/intelligence"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/store"
)

// newCacheFixture 起一个带语义缓存的被测网关：上游计数，便于断言
// 「第二次请求不再触达上游」。第三返回值是被测 Server（供等待异步
// 回写落库的确定性同步，见 waitCacheEntries）。
func newCacheFixture(t *testing.T, stream bool) (gwURL string, upstreamHits *atomic.Int64, srv *Server) {
	t.Helper()
	hits := &atomic.Int64{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: "+`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"model-a","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`+"\n\n")
			io.WriteString(w, "data: "+`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"model-a","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
			io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"model-a",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
	}))
	t.Cleanup(up.Close)

	a, err := openai_compat.New(openai_compat.Spec{ProviderName: "a", BaseURL: up.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), st))
	s.SetRouter(&routing.Router{Providers: []provider.Provider{a}})
	s.SetCache(intelligence.NewSemCache(st, time.Hour, 64))
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	return gw.URL, hits, s
}

// waitCacheEntries 轮询等待异步回写落库（store 计数 ≥ n），不产生
// 上游流量——回写 goroutine 与测试重放之间的确定性同步点。此前
// awaitCacheHit 靠「重放直到命中」等待，但每次未命中的重放都会再打
// 一次上游，污染调用计数断言（macOS 快 runner 上竞态显性化，
// 2026-08-29 CI 首现）。
func waitCacheEntries(t *testing.T, s *Server, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if c, err := s.st.CountSemanticCache(); err == nil && int(c) >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("2s 内回写未落库（want ≥%d 条）", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// awaitCacheHit 轮询重放同一请求直至命中（回写为异步 goroutine，
// 首响应返回时写入可能尚未落库）。返回最终命中响应与该次耗时。
func awaitCacheHit(t *testing.T, url, body string) (*http.Response, time.Duration) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		start := time.Now()
		resp := postAuthed(t, url, body)
		elapsed := time.Since(start)
		if resp.Header.Get("X-OmniFusion-Cache") == "hit" {
			return resp, elapsed
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if time.Now().After(deadline) {
			t.Fatal("2s 内未见缓存命中（回写未落库？）")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSemanticCacheHit(t *testing.T) {
	url, hits, s := newCacheFixture(t, false)
	body := `{"model":"model-a","messages":[{"role":"user","content":"ping"}]}`

	first := postAuthed(t, url+"/v1/chat/completions", body)
	if first.Header.Get("X-OmniFusion-Cache") != "miss" {
		t.Errorf("首次请求 cache 头 = %q, want miss", first.Header.Get("X-OmniFusion-Cache"))
	}
	var firstBody schema.Response
	json.NewDecoder(first.Body).Decode(&firstBody)
	first.Body.Close()
	waitCacheEntries(t, s, 1) // 等异步回写落库（确定性同步，不重放请求）

	second, elapsed := awaitCacheHit(t, url+"/v1/chat/completions", body)
	defer second.Body.Close()

	// 验收（docs/05 4.6）：重复请求 TTFT < 10ms（此处以客户端实测
	// 往返近似，命中路径不含上游）。CI 共享 runner（GITHUB_ACTIONS）
	// 调度抖动可达数十毫秒，放宽到 100ms——断言意图是「命中显著快于
	// 上游往返（秒级）」而非微观时延（本机实测命中 ~0.6ms，见 bench）。
	limit := 10 * time.Millisecond
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		limit = 100 * time.Millisecond
	}
	if elapsed >= limit {
		t.Errorf("命中请求耗时 %v, want <%v", elapsed, limit)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("上游收到 %d 次请求, want 1（第二次应由缓存作答）", n)
	}
	var secondBody schema.Response
	json.NewDecoder(second.Body).Decode(&secondBody)
	if secondBody.ID != firstBody.ID ||
		secondBody.Choices[0].Message.Content.TextOf() != firstBody.Choices[0].Message.Content.TextOf() {
		t.Errorf("命中响应与首次不等价: %q vs %q", secondBody.ID, firstBody.ID)
	}
}

func TestSemanticCacheStreamBypass(t *testing.T) {
	url, hits, _ := newCacheFixture(t, true)
	body := `{"model":"model-a","stream":true,"messages":[{"role":"user","content":"ping"}]}`

	for i := 0; i < 2; i++ {
		resp := postAuthed(t, url+"/v1/chat/completions", body)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.Header.Get("X-OmniFusion-Cache") != "" {
			t.Errorf("流式请求不应携带 cache 头, got %q", resp.Header.Get("X-OmniFusion-Cache"))
		}
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("上游收到 %d 次请求, want 2（流式不查缓存）", n)
	}
}

func TestSemanticCacheCrossProtocol(t *testing.T) {
	url, hits, s := newCacheFixture(t, false)

	// 首问走 Anthropic /v1/messages（x-api-key 鉴权）
	anthropic := `{"model":"model-a","max_tokens":100,"messages":[{"role":"user","content":"ping"}]}`
	first := postMessages(t, url+"/v1/messages", anthropic, testGatewayToken)
	io.Copy(io.Discard, first.Body)
	first.Body.Close()
	waitCacheEntries(t, s, 1) // 等回写落库：此前重放竞态会在 macOS 快 runner 上多打一次上游

	// 同逻辑请求走 OpenAI /v1/chat/completions：IR 相同 → 键相同 → 命中
	openaiBody := `{"model":"model-a","max_tokens":100,"messages":[{"role":"user","content":"ping"}]}`
	second := postAuthed(t, url+"/v1/chat/completions", openaiBody)
	if second.Header.Get("X-OmniFusion-Cache") != "hit" {
		second.Body.Close()
		t.Fatalf("跨协议未命中：cache 头 = %q, want hit", second.Header.Get("X-OmniFusion-Cache"))
	}
	io.Copy(io.Discard, second.Body)
	second.Body.Close()

	if n := hits.Load(); n != 1 {
		t.Errorf("跨协议命中失败：上游收到 %d 次请求, want 1", n)
	}
}
