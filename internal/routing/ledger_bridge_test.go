package routing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/quota"
)

// Ledger 桥的端到端测试：真实 Dispatch 循环里 429/402 自动喂入账本。

func TestDispatchFeedsLedgerOnQuota429(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		// 配额关键词触发 KindQuotaExhausted（errorclass 的 429 语义分支）
		w.Write([]byte(`{"error":{"message":"insufficient_quota: daily limit"}}`))
	}))
	defer up.Close()

	l := quota.NewLedger()
	r := &Router{
		Providers: []provider.Provider{newMockAdapter(t, "alpha", up.URL)},
		Ledger:    l,
	}
	_, _, _ = r.Dispatch(context.Background(), testRequest())
	e := l.Get("alpha", "m")
	if e.State != quota.StateVerifiedFree {
		t.Fatalf("state = %s, want VERIFIED_FREE (429 quota = window exhausted)", e.State)
	}
	if e.Window.Remaining != 0 {
		t.Errorf("remaining = %v, want 0", e.Window.Remaining)
	}
	if e.Source != quota.SourceRuntime429 {
		t.Errorf("source = %s, want runtime_429", e.Source)
	}
}

func TestDispatchFeedsLedgerOn402(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"error":{"message":"billing"}}`))
	}))
	defer up.Close()

	l := quota.NewLedger()
	r := &Router{
		Providers: []provider.Provider{newMockAdapter(t, "beta", up.URL)},
		Ledger:    l,
	}
	_, _, _ = r.Dispatch(context.Background(), testRequest())
	e := l.Get("beta", "m")
	if e.State != quota.StateVerifiedPaid {
		t.Fatalf("state = %s, want VERIFIED_PAID (402 = free layer gone)", e.State)
	}
}

func TestLedgerNilMeansZeroBehaviorChange(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"insufficient_quota"}}`))
	}))
	defer up.Close()

	r := &Router{Providers: []provider.Provider{newMockAdapter(t, "gamma", up.URL)}}
	// Ledger 未装配：Dispatch 行为不变（不 panic、不喂账本）
	_, attempts, err := r.Dispatch(context.Background(), testRequest())
	if err == nil || len(attempts) != 1 {
		t.Fatalf("expected 1 failed attempt, got %d attempts err=%v", len(attempts), err)
	}
	// EntitlementOf 未装配返回 UNKNOWN 零值（不确定不惩罚）
	e := r.EntitlementOf("gamma", "")
	if e.State != quota.StateUnknown {
		t.Errorf("unwired ledger EntitlementOf = %s, want UNKNOWN", e.State)
	}
}

func TestPlainRateLimitNotFed(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limited"}}`)) // 无配额关键词
	}))
	defer up.Close()

	l := quota.NewLedger()
	r := &Router{
		Providers: []provider.Provider{newMockAdapter(t, "delta", up.URL)},
		Ledger:    l,
	}
	_, _, _ = r.Dispatch(context.Background(), testRequest())
	if e := l.Get("delta", "m"); e.State != quota.StateUnknown {
		t.Fatalf("plain 429 must NOT feed ledger, state = %s", e.State)
	}
}

func TestSuccessNotFed(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(okCompletion("m")))
	}))
	defer up.Close()

	l := quota.NewLedger()
	r := &Router{
		Providers: []provider.Provider{newMockAdapter(t, "epsilon", up.URL)},
		Ledger:    l,
	}
	if _, _, err := r.Dispatch(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}
	if e := l.Get("epsilon", "m"); e.State != quota.StateUnknown {
		t.Fatalf("success must not create entitlement evidence, state = %s", e.State)
	}
}
