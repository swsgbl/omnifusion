package server

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/intelligence"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/store"
)

// memoryFixture：单 provider 回显上游（捕获末次请求体）+ 装配 FTS5
// 记忆的被测网关。withMemory=false 时不 SetMemory（nil 守卫面）。
type memoryFixture struct {
	gw       *httptest.Server
	srv      *Server
	up       *httptest.Server
	st       *store.Store
	mu       sync.Mutex
	lastBody string
}

func newMemoryFixture(t *testing.T, withMemory bool) *memoryFixture {
	t.Helper()
	fx := &memoryFixture{}
	fx.up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		fx.mu.Lock()
		fx.lastBody = string(b)
		fx.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"c1","object":"chat.completion","created":1,"model":"model-a",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
	}))
	t.Cleanup(fx.up.Close)

	a, err := openai_compat.New(openai_compat.Spec{
		ProviderName: "a", BaseURL: fx.up.URL + "/v1", APIKey: "k",
	})
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	fx.st, err = store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { fx.st.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := authedServer(New(nil, log, fx.st))
	s.SetRouter(&routing.Router{Providers: []provider.Provider{a}})
	if withMemory {
		s.SetMemory(intelligence.NewMemory(fx.st, log))
	}
	fx.srv = s
	fx.gw = httptest.NewServer(s.Handler())
	t.Cleanup(fx.gw.Close)
	return fx
}

// postMem 带自定义头的已鉴权 POST。
func postMem(t *testing.T, url, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testGatewayToken)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}

func (fx *memoryFixture) body() string {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return fx.lastBody
}

// waitCount 轮询等待记忆行数到达 want（record 是旁路 goroutine）。
func (fx *memoryFixture) waitCount(t *testing.T, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n, err := fx.st.CountSessionMemory(); err == nil && n >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	n, _ := fx.st.CountSessionMemory()
	t.Fatalf("记忆行数未达 %d（现 %d）", want, n)
}

// TestMemoryChatEndToEnd opt-in 全链路：首轮记录（user+assistant 落
// 库）→ 二轮召回注入（上游可见 [memory] 与首轮内容、响应头给命中数）。
func TestMemoryChatEndToEnd(t *testing.T) {
	fx := newMemoryFixture(t, true)
	on := map[string]string{
		"X-Session-Id": "sess-1",
		HeaderMemory:   "on",
	}
	first := `{"model":"model-a","messages":[{"role":"user","content":"我喜欢用 SQLite 存网关记忆"}]}`
	resp := postMem(t, fx.gw.URL+"/v1/chat/completions", first, on)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("首轮状态 = %d", resp.StatusCode)
	}
	resp.Body.Close()
	fx.waitCount(t, 2)

	second := `{"model":"model-a","messages":[{"role":"user","content":"网关记忆方案靠谱吗"}]}`
	resp = postMem(t, fx.gw.URL+"/v1/chat/completions", second, on)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("二轮状态 = %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-OmniFusion-Memory-Hits"); got == "" {
		t.Fatal("二轮缺 X-OmniFusion-Memory-Hits 响应头")
	}
	up := fx.body()
	if !strings.Contains(up, "[memory 1]") {
		t.Fatalf("上游未见注入: %.300s", up)
	}
	if !strings.Contains(up, "SQLite 存网关记忆") {
		t.Fatalf("注入不含首轮内容: %.300s", up)
	}
	if !strings.Contains(up, `"role":"system"`) {
		t.Fatalf("注入非 system 消息: %.300s", up)
	}
}

// TestMemoryOffByDefault 默认关闭：同 session 两轮（无 opt-in 头）零
// 落盘、零注入——隐私红线。
func TestMemoryOffByDefault(t *testing.T) {
	fx := newMemoryFixture(t, true)
	h := map[string]string{"X-Session-Id": "sess-2"}
	for i := 0; i < 2; i++ {
		resp := postMem(t, fx.gw.URL+"/v1/chat/completions",
			`{"model":"model-a","messages":[{"role":"user","content":"网关记忆话题第`+
				string(rune('a'+i))+`轮"}]}`, h)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("第 %d 轮状态 = %d", i+1, resp.StatusCode)
		}
		resp.Body.Close()
	}
	time.Sleep(100 * time.Millisecond) // 留出潜在（不该有的）旁路写入窗口
	if n, _ := fx.st.CountSessionMemory(); n != 0 {
		t.Fatalf("默认关闭仍落盘 %d 行", n)
	}
	if strings.Contains(fx.body(), "[memory") {
		t.Fatal("默认关闭仍注入")
	}
}

// TestMemoryNoSessionNoRecord opt-in 开但无 X-Session-Id：不记录、
// 后续也不召回（无记忆可查）。
func TestMemoryNoSessionNoRecord(t *testing.T) {
	fx := newMemoryFixture(t, true)
	on := map[string]string{HeaderMemory: "on"}
	resp := postMem(t, fx.gw.URL+"/v1/chat/completions",
		`{"model":"model-a","messages":[{"role":"user","content":"网关记忆话题"}]}`, on)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态 = %d", resp.StatusCode)
	}
	resp.Body.Close()
	time.Sleep(100 * time.Millisecond)
	if n, _ := fx.st.CountSessionMemory(); n != 0 {
		t.Fatalf("无 session 仍落盘 %d 行", n)
	}
}

// TestMemoryCrossProtocolRecall 跨协议共享：/v1/chat/completions 首轮
// 记录，/v1/messages 二轮召回注入——记忆挂在 IR 层，协议无关。
func TestMemoryCrossProtocolRecall(t *testing.T) {
	fx := newMemoryFixture(t, true)
	on := map[string]string{"X-Session-Id": "sess-3", HeaderMemory: "on"}
	resp := postMem(t, fx.gw.URL+"/v1/chat/completions",
		`{"model":"model-a","messages":[{"role":"user","content":"部署方案选 Kubernetes"}]}`, on)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("首轮状态 = %d", resp.StatusCode)
	}
	resp.Body.Close()
	fx.waitCount(t, 2)

	resp = postMem(t, fx.gw.URL+"/v1/messages",
		`{"model":"model-a","max_tokens":32,"messages":[{"role":"user","content":"Kubernetes 部署再讲讲"}]}`, on)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("二轮状态 = %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-OmniFusion-Memory-Hits"); got == "" {
		t.Fatal("跨协议召回未命中")
	}
	if !strings.Contains(fx.body(), "[memory 1]") {
		t.Fatalf("上游未见跨协议注入: %.300s", fx.body())
	}
}

// TestMemoryNotAssembled 未装配记忆（SetMemory 未调用）+ opt-in 头：
// 请求照常成功、无注入、无异常（nil 守卫）。
func TestMemoryNotAssembled(t *testing.T) {
	fx := newMemoryFixture(t, false)
	on := map[string]string{"X-Session-Id": "sess-4", HeaderMemory: "on"}
	resp := postMem(t, fx.gw.URL+"/v1/chat/completions",
		`{"model":"model-a","messages":[{"role":"user","content":"anything at all"}]}`, on)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态 = %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-OmniFusion-Memory-Hits"); got != "" {
		t.Fatalf("未装配却给命中头 %q", got)
	}
	time.Sleep(50 * time.Millisecond)
	if n, _ := fx.st.CountSessionMemory(); n != 0 {
		t.Fatalf("未装配仍落盘 %d 行", n)
	}
}
