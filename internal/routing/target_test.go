package routing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
)

// TestDispatchTargetDirect 命中声明的 (provider, model)：不撞候选
// 选择、不 failover、模型名改写生效。
func TestDispatchTargetDirect(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	var gotModel string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits++
		gotModel = extractModelField(string(body))
		mu.Unlock()
		io.WriteString(w, okCompletion("echo"))
	}))
	defer up.Close()

	r := &Router{Providers: []provider.Provider{
		newMockAdapter(t, "a", up.URL),
		newMockAdapter(t, "b", up.URL),
	}}
	resp, attempts, err := r.Dispatch(context.Background(), testRequest(),
		WithTarget("b", "model-b"))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.ProviderName != "b" {
		t.Errorf("ProviderName = %q, want b", resp.ProviderName)
	}
	if len(attempts) != 1 || attempts[0].Provider != "b" {
		t.Errorf("attempts = %+v, want 单条 b", attempts)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Errorf("upstream hits = %d, want 1（定向单发不 failover）", hits)
	}
	if gotModel != "model-b" {
		t.Errorf("上游收到 model = %q, want model-b（成员模型改写）", gotModel)
	}
}

// extractModelField 从请求 JSON 粗取 "model" 字段值（测试专用）。
func extractModelField(body string) string {
	const key = `"model":"`
	i := strings.Index(body, key)
	if i < 0 {
		return ""
	}
	rest := body[i+len(key):]
	if j := strings.Index(rest, `"`); j >= 0 {
		return rest[:j]
	}
	return ""
}

// TestDispatchTargetProviderMissing provider 未装配 → 失败且不静默换家。
func TestDispatchTargetProviderMissing(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, okCompletion("m"))
	}))
	defer up.Close()
	r := &Router{Providers: []provider.Provider{newMockAdapter(t, "a", up.URL)}}
	_, attempts, err := r.Dispatch(context.Background(), testRequest(),
		WithTarget("nope", "m"))
	if err == nil {
		t.Fatal("err = nil, want 未装配失败")
	}
	if len(attempts) != 1 || attempts[0].Provider != "nope" {
		t.Errorf("attempts = %+v, want 单条 nope", attempts)
	}
}

// TestDispatchTargetUpstreamErrorFailsClosed 上游 5xx → 定向语义无
// failover，错误上抛（门控/降级由 Fusion 层接管）。
func TestDispatchTargetUpstreamErrorFailsClosed(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"message":"boom"}}`)
	}))
	defer up.Close()
	r := &Router{Providers: []provider.Provider{
		newMockAdapter(t, "a", up.URL),
		newMockAdapter(t, "b", up.URL),
	}}
	_, attempts, err := r.Dispatch(context.Background(), testRequest(),
		WithTarget("a", "m"))
	if err == nil {
		t.Fatal("err = nil, want 上游错误上抛")
	}
	if len(attempts) != 1 || attempts[0].Provider != "a" {
		t.Errorf("attempts = %+v, want 单条 a（b 不被连带尝试）", attempts)
	}
	if attempts[0].Err == nil || attempts[0].Kind == "" {
		t.Errorf("attempt 应带 Err 与 Kind：%+v", attempts[0])
	}
}
