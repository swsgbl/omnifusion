// dashboard_test.go 覆盖 M4.8 Dashboard v0：鉴权（Bearer / ?key=、
// bare 401）、三页 HTML、根重定向保 key、未知路径 404，以及三个
// JSON 端点的数据形状（router/catalog/scorer/cooldowns、keySources ∪
// connections、Quota 滑窗 + 语义缓存计数）。
package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/store"
)

// newDashFixture 起一个带真 store 的被测网关（鉴权 helper 复用）；
// 返回网关与 Server/store 引用供数据预置。
func newDashFixture(t *testing.T, router *routing.Router) (*httptest.Server, *Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), st))
	s.SetRouter(router)
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	return gw, s, st
}

func getDashboard(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

// TestDashboardRequiresKey 验证控制面 fail-closed：无 key 的页面与
// API 请求一律 401。
func TestDashboardRequiresKey(t *testing.T) {
	gw, _, _ := newDashFixture(t, &routing.Router{})
	for _, path := range []string{
		"/dashboard/providers", "/dashboard/keys", "/dashboard/usage",
		"/dashboard/api/providers", "/dashboard/api/keys", "/dashboard/api/usage",
	} {
		resp := getDashboard(t, gw.URL+path)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s bare = %d, want 401", path, resp.StatusCode)
		}
	}
}

// TestDashboardPagesServeHTML 验证 ?key= 鉴权通过、三页 HTML 输出、
// 根路径重定向保 key、未知路径 404。
func TestDashboardPagesServeHTML(t *testing.T) {
	gw, _, _ := newDashFixture(t, &routing.Router{})
	q := "?key=" + testGatewayToken

	for _, page := range []string{"providers", "keys", "usage", "compression", "resilience"} {
		resp := getDashboard(t, gw.URL+"/dashboard/"+page+q)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("page %s = %d, want 200", page, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("page %s Content-Type = %q, want text/html", page, ct)
		}
		if !strings.Contains(string(body), "OmniFusion") {
			t.Errorf("page %s body missing app title", page)
		}
	}

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := noRedirect.Get(gw.URL + "/dashboard" + q)
	if err != nil {
		t.Fatalf("GET /dashboard: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound ||
		resp.Header.Get("Location") != "/dashboard/providers"+q {
		t.Errorf("root redirect = %d %q, want 302 preserving key", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp = getDashboard(t, gw.URL+"/dashboard/nope"+q)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown page = %d, want 404", resp.StatusCode)
	}
}

// TestDashboardProvidersAPI 验证 providers JSON：router 成员 + scorer
// 观测 + store 活跃隔离。
func TestDashboardProvidersAPI(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"model-a",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(up.Close)
	a, err := openai_compat.New(openai_compat.Spec{ProviderName: "alpha", BaseURL: up.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	scorer := routing.NewScorer()
	scorer.Observe("alpha", 250*time.Millisecond, true)
	gw, _, _ := newDashFixture(t, &routing.Router{
		Providers: []provider.Provider{a}, Scoring: scorer,
	})

	req, _ := http.NewRequest(http.MethodGet, gw.URL+"/dashboard/api/providers", nil)
	req.Header.Set("Authorization", "Bearer "+testGatewayToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET api/providers: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("api/providers = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Providers []struct {
			Name        string   `json:"name"`
			LatencyMS   float64  `json:"latency_ms"`
			Cooldowns   []string `json:"-"`
			LastSuccess *string  `json:"last_success_at"`
		} `json:"providers"`
		ModelsTotal int `json:"models_total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Providers) != 1 || body.Providers[0].Name != "alpha" {
		t.Fatalf("providers = %+v, want one alpha", body.Providers)
	}
	if body.Providers[0].LatencyMS <= 0 {
		t.Errorf("latency_ms = %v, want >0 after Observe", body.Providers[0].LatencyMS)
	}
	if body.Providers[0].LastSuccess == nil {
		t.Error("last_success_at = nil, want timestamp")
	}
	if body.ModelsTotal != 0 {
		t.Errorf("models_total = %d, want 0 (no catalog)", body.ModelsTotal)
	}
}

// TestDashboardKeysAPI 验证 keys JSON：注入来源与 connections 表合并，
// stored 记录带 label/updated_at，env 来源原样透出。
func TestDashboardKeysAPI(t *testing.T) {
	gw, s, st := newDashFixture(t, &routing.Router{})
	s.SetKeySources(map[string]string{
		"groq":  "env:GROQ_API_KEY",
		"local": "-",
	})
	if err := st.SetConnection("groq", []byte{1, 2, 3}, "main"); err != nil {
		t.Fatalf("SetConnection: %v", err)
	}

	resp := getDashboard(t, gw.URL+"/dashboard/api/keys?key="+testGatewayToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("api/keys = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Keys []struct {
			Provider  string `json:"provider"`
			Source    string `json:"source"`
			Label     string `json:"label"`
			UpdatedAt string `json:"updated_at"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Keys) != 2 {
		t.Fatalf("keys = %+v, want 2 rows", body.Keys)
	}
	var groq, local *struct {
		Provider  string `json:"provider"`
		Source    string `json:"source"`
		Label     string `json:"label"`
		UpdatedAt string `json:"updated_at"`
	}
	for i := range body.Keys {
		switch body.Keys[i].Provider {
		case "groq":
			groq = &body.Keys[i]
		case "local":
			local = &body.Keys[i]
		}
	}
	if groq == nil || groq.Source != "stored" || groq.Label != "main" || groq.UpdatedAt == "" {
		t.Errorf("groq key = %+v, want stored/main with updated_at", groq)
	}
	if local == nil || local.Source != "-" {
		t.Errorf("local key = %+v, want source -", local)
	}
}

// TestDashboardUsageAPI 验证 usage JSON：Quota 滑窗快照（用量/限值/
// headroom）与语义缓存计数。
func TestDashboardUsageAPI(t *testing.T) {
	qt := routing.NewQuotaTracker()
	qt.SetLimit("groq", routing.QuotaLimits{RPM: 30})
	qt.RecordRequest("groq")
	qt.RecordTokens("groq", 120)
	gw, _, st := newDashFixture(t, &routing.Router{Quota: qt})
	if err := st.PutSemanticCache("h1", []byte(`{}`), time.Now().Unix()); err != nil {
		t.Fatalf("PutSemanticCache: %v", err)
	}

	resp := getDashboard(t, gw.URL+"/dashboard/api/usage?key="+testGatewayToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("api/usage = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Usage []struct {
			Provider string `json:"provider"`
			RPM      int    `json:"rpm"`
			Limits   struct {
				RPM int `json:"rpm"`
			} `json:"limits"`
			Headroom float64 `json:"headroom"`
		} `json:"usage"`
		CacheEntries int64 `json:"cache_entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Usage) != 1 {
		t.Fatalf("usage = %+v, want 1 row", body.Usage)
	}
	u := body.Usage[0]
	if u.Provider != "groq" || u.RPM != 1 || u.Limits.RPM != 30 {
		t.Errorf("groq usage = %+v, want rpm=1 limit=30", u)
	}
	if u.Headroom <= 0.9 || u.Headroom > 1 {
		t.Errorf("groq headroom = %v, want ~0.967", u.Headroom)
	}
	if body.CacheEntries != 1 {
		t.Errorf("cache_entries = %d, want 1", body.CacheEntries)
	}
}
