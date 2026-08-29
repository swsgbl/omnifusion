package routing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/store"
)

// fakeClock 提供可控时钟（iso.now 注入点）。
type fakeClock struct{ t time.Time }

func newFakeClock() *fakeClock           { return &fakeClock{t: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)} }
func (f *fakeClock) Now() time.Time      { return f.t }
func (f *fakeClock) Add(d time.Duration) { f.t = f.t.Add(d) }

func newTestIsolation(t *testing.T) (*Isolation, *fakeClock) {
	t.Helper()
	iso, err := NewIsolation(nil, nil)
	if err != nil {
		t.Fatalf("NewIsolation: %v", err)
	}
	fc := newFakeClock()
	iso.now = fc.Now
	return iso, fc
}

func TestConnectionCooldownBlocksAndExpires(t *testing.T) {
	iso, fc := newTestIsolation(t)
	iso.ApplyFailure("groq", "llama-x", KindRateLimit)

	if blocked, reason := iso.Block("groq"); !blocked || !strings.Contains(reason, "cooldown") {
		t.Fatalf("Block = %v %q, want cooldown", blocked, reason)
	}
	if blocked, _ := iso.Block("other"); blocked {
		t.Error("cooldown must be provider-scoped")
	}

	fc.Add(RateLimitCooldown) // 到点
	if blocked, _ := iso.Block("groq"); blocked {
		t.Error("cooldown must expire")
	}
}

func TestAuthInvalidCooldownIsLong(t *testing.T) {
	iso, fc := newTestIsolation(t)
	iso.ApplyFailure("p", "m", KindAuthInvalid)

	fc.Add(RateLimitCooldown) // 短冷却已过
	if blocked, _ := iso.Block("p"); !blocked {
		t.Error("auth_invalid must outlast rate_limit cooldown")
	}
	fc.Add(AuthInvalidCooldown)
	if blocked, _ := iso.Block("p"); blocked {
		t.Error("auth_invalid cooldown must expire after 30m")
	}
}

func TestQuotaLockoutScopedToModel(t *testing.T) {
	iso, _ := newTestIsolation(t)
	iso.ApplyFailure("groq", "llama-x", KindQuotaExhausted)

	if blocked, _ := iso.Block("groq"); blocked {
		t.Error("quota lockout must not cool the whole connection")
	}
	if locked, reason := iso.LockedModel("groq", "llama-x"); !locked || !strings.Contains(reason, "locked") {
		t.Fatalf("LockedModel = %v %q", locked, reason)
	}
	if locked, _ := iso.LockedModel("groq", "other-model"); locked {
		t.Error("lockout must be model-scoped")
	}
	if locked, _ := iso.LockedModel("openrouter", "llama-x"); locked {
		t.Error("lockout must be provider-scoped")
	}
}

func TestUnknownKindDoesNothing(t *testing.T) {
	iso, fc := newTestIsolation(t)
	iso.ApplyFailure("p", "m", KindUnknown)
	iso.ApplyFailure("p", "m", KindRequestError)
	iso.ApplyFailure("p", "m", "")
	fc.Add(time.Second)
	if blocked, _ := iso.Block("p"); blocked {
		t.Error("request errors must not isolate the provider")
	}
	if locked, _ := iso.LockedModel("p", "m"); locked {
		t.Error("request errors must not lock models")
	}
}

func TestBreakerTripsOn5xxRate(t *testing.T) {
	iso, fc := newTestIsolation(t)
	for i := 0; i < breakerFailThresh; i++ {
		iso.ApplyFailure("p", "m", KindUpstream5xx)
	}
	blocked, reason := iso.Block("p")
	if !blocked || !strings.Contains(reason, "breaker") {
		t.Fatalf("Block = %v %q, want breaker open", blocked, reason)
	}

	// 到点转 half-open：放行一次探测
	fc.Add(breakerOpenBase + time.Second)
	if blocked, _ := iso.Block("p"); blocked {
		t.Fatal("half-open probe must be allowed through")
	}
	// 探测在途：其余请求仍被隔离（单探测语义）
	if blocked, _ := iso.Block("p"); !blocked {
		t.Fatal("second probe during half-open must stay blocked")
	}
	// 探测成功 → 闭合
	iso.ApplySuccess("p")
	if blocked, _ := iso.Block("p"); blocked {
		t.Fatal("breaker must close after successful probe")
	}
}

func TestBreakerProbeFailureDoublesBackoff(t *testing.T) {
	iso, fc := newTestIsolation(t)
	for i := 0; i < breakerFailThresh; i++ {
		iso.ApplyFailure("p", "m", KindNetOrTimeout)
	}
	fc.Add(breakerOpenBase + time.Second)
	iso.Block("p") // open → half-open（放行探测）
	iso.ApplyFailure("p", "m", KindNetOrTimeout)

	blocked, _ := iso.Block("p")
	if !blocked {
		t.Fatal("failed probe must re-open the breaker")
	}
	fc.Add(breakerOpenBase) // 首轮退避时长不够（已翻倍）
	if blocked, _ := iso.Block("p"); !blocked {
		t.Fatal("backoff must have doubled after failed probe")
	}
	fc.Add(breakerOpenBase) // 累计 2×base：放行
	if blocked, _ := iso.Block("p"); blocked {
		t.Fatal("doubled backoff must now allow the probe")
	}
}

func TestRateLimitDoesNotTripBreaker(t *testing.T) {
	iso, fc := newTestIsolation(t)
	for i := 0; i < breakerWindow+5; i++ {
		iso.ApplyFailure("p", "m", KindRateLimit)
	}
	fc.Add(2 * RateLimitCooldown) // 冷却过期
	if blocked, _ := iso.Block("p"); blocked {
		t.Error("rate limits must never trip the breaker")
	}
}

// TestIsolationSurvivesRestart 是 M2.2 验收（FreeRide 教训）：
// 冷却/锁定状态重启不丢。
func TestIsolationSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "iso.db")

	st1, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	iso1, err := NewIsolation(st1, nil)
	if err != nil {
		t.Fatalf("NewIsolation: %v", err)
	}
	base := time.Now().UTC()
	iso1.now = func() time.Time { return base }
	iso1.ApplyFailure("groq", "llama-x", KindRateLimit)
	iso1.ApplyFailure("openrouter", "m2", KindQuotaExhausted)
	st1.Close()

	// "重启"：同一文件重建 store + 状态机，时钟仍停在原点
	st2, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st2.Close()
	iso2, err := NewIsolation(st2, nil)
	if err != nil {
		t.Fatalf("NewIsolation(restart): %v", err)
	}
	iso2.now = func() time.Time { return base.Add(time.Second) }

	if blocked, reason := iso2.Block("groq"); !blocked || !strings.Contains(reason, "cooldown") {
		t.Errorf("cooldown lost after restart: %v %q", blocked, reason)
	}
	if locked, _ := iso2.LockedModel("openrouter", "m2"); !locked {
		t.Error("model lockout lost after restart")
	}
	if locked, _ := iso2.LockedModel("openrouter", "other"); locked {
		t.Error("restored lockout must stay model-scoped")
	}
}

// TestDispatchSkipsIsolatedProvider 验证 Router 集成：429 冷却后
// 下一请求直接跳过该 provider。
func TestDispatchSkipsIsolatedProvider(t *testing.T) {
	hits := map[string]int{}
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits["a"]++
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":"rate limited"}`)
	}))
	defer upA.Close()
	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits["b"]++
		io.WriteString(w, okCompletion("model-b"))
	}))
	defer upB.Close()

	iso, _ := NewIsolation(nil, nil)
	r := &Router{
		Providers: []provider.Provider{
			newMockAdapter(t, "a", upA.URL),
			newMockAdapter(t, "b", upB.URL),
		},
		Isolation: iso,
	}

	if _, _, err := r.Dispatch(context.Background(), testRequest()); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if hits["a"] != 1 || hits["b"] != 1 {
		t.Fatalf("first dispatch hits = %v", hits)
	}

	resp, attempts, err := r.Dispatch(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	if resp.ProviderName != "b" {
		t.Errorf("second dispatch provider = %q", resp.ProviderName)
	}
	if hits["a"] != 1 {
		t.Errorf("cooled provider must not be hit again (hits=%v)", hits)
	}
	if len(attempts) != 2 || attempts[0].Err == nil || !strings.Contains(attempts[0].Err.Error(), "skipped") {
		t.Fatalf("attempts = %+v, want skip record first", attempts)
	}
}

// TestDispatchModelLockoutFallsThrough 验证 Model 层隔离：
// 配额锁定的模型在 Translate 后被跳过，同 provider 其他模型不受影响
// （此处直接验证 att 记录语义）。
func TestDispatchModelLockoutFallsThrough(t *testing.T) {
	hits := 0
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		io.WriteString(w, okCompletion("model-a"))
	}))
	defer upA.Close()

	iso, _ := NewIsolation(nil, nil)
	iso.ApplyFailure("a", "m", KindQuotaExhausted) // 锁定 provider a 的模型 m
	r := &Router{Providers: []provider.Provider{newMockAdapter(t, "a", upA.URL)}, Isolation: iso}

	_, attempts, err := r.Dispatch(context.Background(), testRequest())
	if err == nil {
		t.Fatal("expected dispatch failure (model locked, no fallback)")
	}
	if hits != 0 {
		t.Errorf("locked model must not be hit upstream (%d hits)", hits)
	}
	if len(attempts) != 1 || !strings.Contains(attempts[0].Err.Error(), "model isolated") {
		t.Fatalf("attempts = %+v", attempts)
	}
}
