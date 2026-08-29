// resilience_api_test.go 覆盖 M5.6 弹性状态面：store 冷却（含 reason）+
// 内存熔断 + scorer 信号 + 审计失败行聚合与空 pin，以及 scope 鉴权
// （health-scoped 403 / route-scoped 200 / bare 401）。
package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/config"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/store"
)

// newResiFixture 起一个装配真 store + 隔离状态机 + 打分器的被测网关。
func newResiFixture(t *testing.T) (*httptest.Server, *routing.Router, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "resi.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	iso, err := routing.NewIsolation(st, nil)
	if err != nil {
		t.Fatalf("NewIsolation: %v", err)
	}
	router := &routing.Router{Isolation: iso, Scoring: routing.NewScorer()}
	cfg := &config.Config{Audit: config.AuditConfig{Enabled: true, MaxRows: 10000}}
	s := authedServer(New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), st))
	s.SetRouter(router)
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	return gw, router, st
}

// resiResp 取弹性状态 API。
func resiResp(t *testing.T, gw *httptest.Server, token string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, gw.URL+"/dashboard/api/resilience", nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get resilience: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.StatusCode, body
}

func TestResilienceAggregatesState(t *testing.T) {
	gw, router, st := newResiFixture(t)

	// store 落一条 model 锁定（含 reason）。
	if err := st.UpsertCooldown(store.Cooldown{
		ScopeType: "model", Provider: "mock", Model: "m1",
		Until: time.Now().Add(10 * time.Minute), Reason: "quota_exhausted"}); err != nil {
		t.Fatalf("UpsertCooldown: %v", err)
	}
	// 熔断：5 连续 5xx（隔离状态机内存态）。
	for i := 0; i < 5; i++ {
		router.Isolation.ApplyFailure("mock", "m1", routing.KindUpstream5xx)
	}
	// 打分信号：一次成功观测（EWMA）。
	router.Scoring.Observe("mock", 120*time.Millisecond, true)
	// 审计失败行。
	if err := st.InsertRequestLog(store.RequestLog{
		TS: time.Now().Unix(), Endpoint: "chat", Model: "m1", Provider: "none",
		Status: 502, ErrKind: "upstream_5xx"}); err != nil {
		t.Fatalf("InsertRequestLog: %v", err)
	}

	status, body := resiResp(t, gw, testGatewayToken)
	if status != http.StatusOK {
		t.Fatalf("resilience = %d, want 200", status)
	}
	if pinned, _ := body["pinned"].(string); pinned != "" {
		t.Errorf("pinned = %q, want empty default", pinned)
	}
	cds, _ := body["cooldowns"].([]any)
	if len(cds) != 1 {
		t.Fatalf("cooldowns = %v, want 1", cds)
	}
	cd := cds[0].(map[string]any)
	if cd["provider"] != "mock" || cd["scope"] != "model" || cd["reason"] != "quota_exhausted" {
		t.Errorf("cooldown row = %v", cd)
	}
	brs, _ := body["breakers"].([]any)
	if len(brs) != 1 {
		t.Fatalf("breakers = %v, want [mock open]", brs)
	}
	br := brs[0].(map[string]any)
	if br["provider"] != "mock" || br["state"] != "open" {
		t.Errorf("breaker row = %v", br)
	}
	if br["failures"].(float64) != 5 || br["open_till"] == nil {
		t.Errorf("breaker row = %v, want failures=5 + open_till", br)
	}
	kinds, _ := body["failure_kinds"].([]any)
	if len(kinds) != 1 || kinds[0].(map[string]any)["kind"] != "upstream_5xx" {
		t.Fatalf("failure_kinds = %v, want [upstream_5xx]", kinds)
	}
	rows, _ := body["failure_rows"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["status"].(float64) != 502 {
		t.Fatalf("failure_rows = %v, want [502]", rows)
	}
}

func TestResilienceScope(t *testing.T) {
	gw, _, _ := newResiFixture(t)
	if status, _ := resiResp(t, gw, DeriveMCPToken(testGatewayToken, []string{ScopeHealth})); status != http.StatusForbidden {
		t.Errorf("health-scoped resilience = %d, want 403", status)
	}
	if status, _ := resiResp(t, gw, DeriveMCPToken(testGatewayToken, []string{ScopeRoute})); status != http.StatusOK {
		t.Errorf("route-scoped resilience = %d, want 200", status)
	}
	req, err := http.NewRequest(http.MethodGet, gw.URL+"/dashboard/api/resilience", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bare resilience = %d, want 401", resp.StatusCode)
	}
}
