package routing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newSessionRouter 装配启用 sticky session 的双 provider 路由。
func newSessionRouter(t *testing.T) *Router {
	t.Helper()
	r := newScoringRouter(t, "a", "b")
	r.Sessions = NewSessionTracker()
	return r
}

// TestStickySessionHoldsAcrossStrategyShift 是 M2.7 验收（同会话粘住
// 同 provider）：策略序已偏向 b，但同会话仍先打绑定的 a。
func TestStickySessionHoldsAcrossStrategyShift(t *testing.T) {
	r := newSessionRouter(t)

	resp, _, err := r.Dispatch(context.Background(), testRequest(), WithSession("s1"))
	if err != nil {
		t.Fatalf("dispatch 1: %v", err)
	}
	if resp.ProviderName != "a" { // 无观测 tie → 注册序
		t.Fatalf("dispatch 1 provider = %q, want a", resp.ProviderName)
	}

	r.Scoring.Observe("a", 3*time.Second, true) // a 变慢：策略序应偏向 b
	if got := r.ordered("", "", 0); got[0].Name() != "b" {
		t.Fatalf("auto order = [%s ...], want b first after a slows down", got[0].Name())
	}

	resp, _, err = r.Dispatch(context.Background(), testRequest(), WithSession("s1"))
	if err != nil {
		t.Fatalf("dispatch 2 (same session): %v", err)
	}
	if resp.ProviderName != "a" {
		t.Fatalf("same session provider = %q, want a (sticky)", resp.ProviderName)
	}

	resp, _, err = r.Dispatch(context.Background(), testRequest()) // 无会话
	if err != nil {
		t.Fatalf("dispatch 3 (no session): %v", err)
	}
	if resp.ProviderName != "b" {
		t.Fatalf("no-session provider = %q, want b (strategy order)", resp.ProviderName)
	}
}

// TestStickySessionUntilBlocked 是 M2.7 验收（直至冷却）：绑定的 a 被
// 配额阻断后让位给 b，且会话重绑到 b。
func TestStickySessionUntilBlocked(t *testing.T) {
	r := newSessionRouter(t)
	qt := NewQuotaTracker()
	qt.SetLimit("a", QuotaLimits{RPM: 1})
	r.Quota = qt

	if _, _, err := r.Dispatch(context.Background(), testRequest(), WithSession("s1")); err != nil {
		t.Fatalf("dispatch 1: %v", err)
	} // s1 → a，且 a 的 RPM 用满

	resp, attempts, err := r.Dispatch(context.Background(), testRequest(), WithSession("s1"))
	if err != nil {
		t.Fatalf("dispatch 2: %v", err)
	}
	if resp.ProviderName != "b" {
		t.Fatalf("dispatch 2 provider = %q, want b (a quota-blocked)", resp.ProviderName)
	}
	if len(attempts) != 2 || attempts[0].Provider != "a" || attempts[0].Err == nil {
		t.Fatalf("attempts = %+v, want a skip record then b success", attempts)
	}

	// 已重绑：第三次同会话直接打 b，不再尝试 a。
	resp, attempts, err = r.Dispatch(context.Background(), testRequest(), WithSession("s1"))
	if err != nil {
		t.Fatalf("dispatch 3: %v", err)
	}
	if resp.ProviderName != "b" || len(attempts) != 1 || attempts[0].Provider != "b" {
		t.Fatalf("dispatch 3 = %q/%+v, want single b attempt (rebound)", resp.ProviderName, attempts)
	}
}

// TestStickySessionTTLExpiry 绑定 30 分钟滑动过期后回落策略序。
func TestStickySessionTTLExpiry(t *testing.T) {
	r := newSessionRouter(t)
	fc := newFakeClock()
	r.Sessions.now = fc.Now

	if _, _, err := r.Dispatch(context.Background(), testRequest(), WithSession("s1")); err != nil {
		t.Fatalf("dispatch 1: %v", err)
	} // s1 → a（tie 注册序）
	r.Scoring.Observe("a", 3*time.Second, true) // a 沉底（未观测 b 取乐观满分，观测 b 反而吃亏）
	if got := r.ordered("", "", 0); got[0].Name() != "b" {
		t.Fatalf("auto order = [%s ...], want b first after a slows down", got[0].Name())
	}

	fc.Add(sessionTTL + time.Minute) // 过期
	if _, ok := r.Sessions.Bound("s1"); ok {
		t.Fatal("expired binding must be forgotten")
	}
	resp, _, err := r.Dispatch(context.Background(), testRequest(), WithSession("s1"))
	if err != nil {
		t.Fatalf("dispatch 2: %v", err)
	}
	if resp.ProviderName != "b" {
		t.Fatalf("expired session provider = %q, want b (strategy order)", resp.ProviderName)
	}
}

// TestSessionTrackerBindNilSafety 空会话/未启用是 no-op。
func TestSessionTrackerNilSafety(t *testing.T) {
	var s *SessionTracker
	if _, ok := s.Bound("x"); ok {
		t.Fatal("nil tracker must report unbound")
	}
	s.Bind("x", "a") // 不应 panic

	r := newSessionRouter(t)
	r.Sessions = nil
	if got := r.applySticky(r.Providers, "s1"); len(got) != 2 {
		t.Fatalf("nil sessions applySticky = %v", got)
	}
}

// newSessionStreamRouter 装配流式可用的双 provider sticky 路由。
// newScoringRouter 的上游只回非流式 JSON，流式解析必败，故专用 SSE
// 上游（chunk 内容标记来源，供测试直接断言选中了谁）。
func newSessionStreamRouter(t *testing.T) *Router {
	t.Helper()
	r := &Router{Scoring: NewScorer()}
	for _, name := range []string{"a", "b"} {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sseBody(chunkPayload("from-"+name)))
		}))
		t.Cleanup(up.Close)
		r.Providers = append(r.Providers, newMockAdapter(t, name, up.URL))
	}
	r.Sessions = NewSessionTracker()
	return r
}

// TestStickyStreamBinds 流式路径同样绑定会话且粘住。
func TestStickyStreamBinds(t *testing.T) {
	r := newSessionStreamRouter(t)

	stream, _, err := r.DispatchStream(context.Background(), streamRequest(), WithSession("s1"))
	if err != nil {
		t.Fatalf("stream 1: %v", err)
	}
	if chunks := collectStream(t, stream); len(chunks) != 1 || chunks[0] != "from-a" {
		t.Fatalf("stream 1 chunks = %v, want [from-a]", chunks)
	}
	if p, ok := r.Sessions.Bound("s1"); !ok || p != "a" {
		t.Fatalf("bound = %q/%v, want a", p, ok)
	}

	r.Scoring.Observe("a", 3*time.Second, true) // 策略序偏向 b
	stream, _, err = r.DispatchStream(context.Background(), streamRequest(), WithSession("s1"))
	if err != nil {
		t.Fatalf("stream 2: %v", err)
	}
	if chunks := collectStream(t, stream); len(chunks) != 1 || chunks[0] != "from-a" {
		t.Fatalf("stream 2 chunks = %v, want [from-a] (sticky on stream path)", chunks)
	}
	if p, ok := r.Sessions.Bound("s1"); !ok || p != "a" {
		t.Fatalf("bound = %q/%v, want a (sticky on stream path)", p, ok)
	}
}
