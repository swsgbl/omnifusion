package routing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/provider"
)

// TestQuotaHeadroom 验证四窗口最紧剩余比例的计算口径。
func TestQuotaHeadroom(t *testing.T) {
	qt, _ := newQuotaWithClock(t)
	if h := qt.Headroom("no-limit"); h != 1 {
		t.Fatalf("headroom(no-limit) = %v, want 1", h)
	}

	qt.SetLimit("p", QuotaLimits{RPM: 10, RPD: 100})
	qt.RecordRequest("p")
	if h := qt.Headroom("p"); h < 0.89 || h > 0.91 { // min(1-1/10, 1-1/100)
		t.Fatalf("headroom = %v, want 0.9 (rpm is tightest)", h)
	}

	for i := 0; i < 8; i++ { // 累计 9/10
		qt.RecordRequest("p")
	}
	if h := qt.Headroom("p"); h > 0.11 {
		t.Fatalf("headroom = %v, want 0.1", h)
	}
	qt.RecordRequest("p") // 10/10 触顶
	if h := qt.Headroom("p"); h != 0 {
		t.Fatalf("headroom = %v, want 0", h)
	}
}

// TestScorerFirstObservationLandsDirectly 首次观测直接落地（EWMA 不从
// 0/1 混合起步），其后按新息权重混合；未观测 key 取乐观默认。
func TestScorerFirstObservationLandsDirectly(t *testing.T) {
	s := NewScorer()
	s.Observe("p", 2*time.Second, false)
	ms, succ := s.Snapshot("p")
	if ms != 2000 || succ != 0 {
		t.Fatalf("snapshot = %v/%v, want 2000/0 (direct)", ms, succ)
	}

	s.Observe("p", 0, true) // blend: 0.7*2000+0.3*0；0.8*0+0.2*1
	if ms, succ = s.Snapshot("p"); ms != 1400 || succ != 0.2 {
		t.Fatalf("snapshot = %v/%v, want 1400/0.2", ms, succ)
	}

	if ms, succ := s.Snapshot("unseen"); ms != 0 || succ != 1 {
		t.Fatalf("unseen = %v/%v, want 0/1 (optimistic defaults)", ms, succ)
	}
}

// newScoringRouter 装配带 Scorer 的双/多 provider 路由（全部 200 OK）。
func newScoringRouter(t *testing.T, names ...string) *Router {
	t.Helper()
	r := &Router{Scoring: NewScorer()}
	for _, n := range names {
		name := n
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, okCompletion("model-"+name))
		}))
		t.Cleanup(up.Close)
		r.Providers = append(r.Providers, newMockAdapter(t, name, up.URL))
	}
	return r
}

// TestRankKeepsRegistryOrderWithoutSignal 无观测时稳定排序保持注册序；
// Scorer 未启用时原样返回（行为）。
func TestRankKeepsRegistryOrderWithoutSignal(t *testing.T) {
	r := newScoringRouter(t, "a", "b")
	got := r.rank()
	if got[0].Name() != "a" || got[1].Name() != "b" {
		t.Fatalf("rank = [%s %s], want [a b] (tie keeps registry order)", got[0].Name(), got[1].Name())
	}

	r.Scoring = nil
	if got = r.rank(); got[0].Name() != "a" || len(got) != 2 {
		t.Fatalf("nil scorer rank = [%s ...], want registry order", got[0].Name())
	}
}

// TestRankSinksSlowAndFailed 延迟差与健康度差都能把劣化者沉底。
func TestRankSinksSlowAndFailed(t *testing.T) {
	r := newScoringRouter(t, "a", "b")
	r.Scoring.Observe("a", 3*time.Second, true) // 慢但健康
	r.Scoring.Observe("b", 10*time.Millisecond, true)
	if got := r.rank(); got[0].Name() != "b" {
		t.Fatalf("rank = [%s ...], want b first (latency)", got[0].Name())
	}

	r2 := newScoringRouter(t, "a", "b")
	r2.Scoring.Observe("a", 10*time.Millisecond, false) // 快但失败（429 形态）
	r2.Scoring.Observe("b", 10*time.Millisecond, true)
	if got := r2.rank(); got[0].Name() != "b" {
		t.Fatalf("rank = [%s ...], want b first (health)", got[0].Name())
	}
}

// TestDispatchSinks429Provider 是 验收（"压测中 429 自动沉底"的
// 单机缩影）：a 持续 429，一次失败观测后 a 沉底，后续分发不再先打它。
func TestDispatchSinks429Provider(t *testing.T) {
	hits := map[string]int{}
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits["a"]++
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"rate limit exceeded"}}`)
	}))
	defer upA.Close()
	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits["b"]++
		io.WriteString(w, okCompletion("model-b"))
	}))
	defer upB.Close()

	r := &Router{
		Providers: []provider.Provider{
			newMockAdapter(t, "a", upA.URL),
			newMockAdapter(t, "b", upB.URL),
		},
		Scoring: NewScorer(),
	}

	if _, _, err := r.Dispatch(context.Background(), testRequest()); err != nil {
		t.Fatalf("first dispatch (failover a→b): %v", err)
	}
	if hits["a"] != 1 || hits["b"] != 1 {
		t.Fatalf("first dispatch hits = %v, want a=1 b=1", hits)
	}

	resp, attempts, err := r.Dispatch(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	if resp.ProviderName != "b" {
		t.Fatalf("second dispatch provider = %q, want b", resp.ProviderName)
	}
	if len(attempts) != 1 || attempts[0].Provider != "b" || attempts[0].Err != nil {
		t.Fatalf("attempts = %+v, want single b attempt (a sank)", attempts)
	}
	if hits["a"] != 1 {
		t.Fatalf("429 provider must sink, hits = %v", hits)
	}
}

// TestDispatchQuotaHeadroomSinks 配额余量参与排序：a 打到 1/2 RPM 后，
// 即使延迟与健康度持平，余量差把 a 沉底（429 之前就避让）。
func TestDispatchQuotaHeadroomSinks(t *testing.T) {
	r := newScoringRouter(t, "a", "b")
	qt := NewQuotaTracker()
	qt.SetLimit("a", QuotaLimits{RPM: 2})
	r.Quota = qt

	resp, _, err := r.Dispatch(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("dispatch 1: %v", err)
	}
	if resp.ProviderName != "a" {
		t.Fatalf("dispatch 1 provider = %q, want a (tie keeps registry order)", resp.ProviderName)
	}

	// dispatch 2 与 3：a 余量 1/2（headroom 0.5），b 未设限（1.0）；
	// 0.2 权重 × 0.5 余量差远超本地延迟噪声，b 应连续优先。
	for i := 2; i <= 3; i++ {
		resp, _, err := r.Dispatch(context.Background(), testRequest())
		if err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
		if resp.ProviderName != "b" {
			t.Fatalf("dispatch %d provider = %q, want b (quota headroom sink)", i, resp.ProviderName)
		}
	}
}
