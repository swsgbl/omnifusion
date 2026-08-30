// audit_api_test.go 是 查询 API 验收：scope 矩阵（401/403/200）
// 与 limit/provider/endpoint/since 过滤、TS RFC3339 形状。
package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/swsgbl/omnifusion/internal/config"
	"github.com/swsgbl/omnifusion/internal/store"
)

// newAuditAPIFixture 装配仅含 store 的网关（无上游，直接种审计行）。
func newAuditAPIFixture(t *testing.T, seed []store.RequestLog) *httptest.Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for i, r := range seed {
		if err := st.InsertRequestLog(r); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	cfg := &config.Config{Audit: config.AuditConfig{Enabled: true}}
	s := authedServer(New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), st))
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	return gw
}

// getAudit 带 token 打 GET /dashboard/api/audit。
func getAudit(t *testing.T, url, token string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAuditAPIAuthMatrix(t *testing.T) {
	gw := newAuditAPIFixture(t, nil)
	if code, _ := getAudit(t, gw.URL+"/dashboard/api/audit", ""); code != http.StatusUnauthorized {
		t.Errorf("bare = %d, want 401", code)
	}
	healthTok := DeriveMCPToken(testGatewayToken, []string{ScopeHealth})
	if code, _ := getAudit(t, gw.URL+"/dashboard/api/audit", healthTok); code != http.StatusForbidden {
		t.Errorf("health-scoped = %d, want 403", code)
	}
	auditTok := DeriveMCPToken(testGatewayToken, []string{ScopeAudit})
	if code, out := getAudit(t, gw.URL+"/dashboard/api/audit", auditTok); code != http.StatusOK {
		t.Errorf("audit-scoped = %d, want 200", code)
	} else if _, ok := out["requests"]; !ok {
		t.Errorf("body missing requests key: %v", out)
	}
	if code, _ := getAudit(t, gw.URL+"/dashboard/api/audit", testGatewayToken); code != http.StatusOK {
		t.Errorf("master = %d, want 200", code)
	}
}

func TestAuditAPIFiltersAndShape(t *testing.T) {
	gw := newAuditAPIFixture(t, []store.RequestLog{
		{TS: 100, Endpoint: "chat", Model: "m", Provider: "groq", Status: 200, TokensIn: 4, TTFTMS: -1},
		{TS: 200, Endpoint: "messages", Model: "m", Provider: "cache", Status: 200, CacheHit: true, TTFTMS: -1},
		{TS: 300, Endpoint: "chat", Model: "m", Provider: "none", Status: 400, ErrKind: "guardrails", TTFTMS: -1},
	})

	// provider 过滤：只命中 groq 行。
	_, out := getAudit(t, gw.URL+"/dashboard/api/audit?provider=groq", testGatewayToken)
	reqs := out["requests"].([]any)
	if len(reqs) != 1 {
		t.Fatalf("provider filter rows = %d, want 1", len(reqs))
	}
	row := reqs[0].(map[string]any)
	if row["provider"] != "groq" || row["endpoint"] != "chat" {
		t.Errorf("row = %v", row)
	}
	// TS 形状：RFC3339（Z 结尾）。
	if ts, _ := row["ts"].(string); len(ts) < 11 || ts[len(ts)-1:] != "Z" {
		t.Errorf("ts = %q, want RFC3339 UTC", ts)
	}
	// 非 0 ttft 语义透传。
	if row["ttft_ms"].(float64) != -1 {
		t.Errorf("ttft_ms = %v, want -1", row["ttft_ms"])
	}

	// endpoint 过滤 + limit：chat 两行取最新 1 行。
	_, out = getAudit(t, gw.URL+"/dashboard/api/audit?endpoint=chat&limit=1", testGatewayToken)
	reqs = out["requests"].([]any)
	if len(reqs) != 1 || reqs[0].(map[string]any)["status"].(float64) != 400 {
		t.Errorf("endpoint+limit rows = %v", reqs)
	}

	// since 过滤。
	_, out = getAudit(t, gw.URL+"/dashboard/api/audit?since=250", testGatewayToken)
	if n := len(out["requests"].([]any)); n != 1 {
		t.Errorf("since rows = %d, want 1", n)
	}
}
