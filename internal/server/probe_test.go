// probe_test.go 覆盖模型可用性探测：五态保守分类纪律 + 端点 e2e
//（master-only、目录模型探测、结论返回）。
package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
)

func TestProbeVerdictOf(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		err    error
		want   string
	}{
		{"2xx", 200, `{"choices":[{}]}`, nil, ProbeAvailable},
		{"404 named", 404, `{"error":{"message":"model not found"}}`, nil, ProbeUnavailable},
		{"400 unsupported", 400, `{"error":{"message":"Model is unavailable"}}`, nil, ProbeUnavailable},
		{"422 no such", 422, `{"error":{"message":"no such model id"}}`, nil, ProbeUnavailable},
		{"429", 429, `{"error":{"message":"rate limited"}}`, nil, ProbeThrottled},
		{"503 service unavailable", 503, "Service Unavailable", nil, ProbeUnknown}, // 反代短语不定罪
		{"401", 401, `{"error":{"message":"bad key"}}`, nil, ProbeUnknown},
		{"400 generic", 400, `{"error":{"message":"context too long"}}`, nil, ProbeUnknown},
		{"transport", 0, "", context.DeadlineExceeded, ProbeUnknown},
	}
	for _, tc := range cases {
		got, _ := probeVerdictOf(tc.status, tc.body, tc.err)
		if got != tc.want {
			t.Errorf("%s: verdict = %s, want %s", tc.name, got, tc.want)
		}
	}
}

// newProbeUpstream 三模型假上游：GET /v1/models 出清单；
// POST 按模型分派：ok=200、gone=404 命名拒收、其余=429。
func newProbeUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[{"id":"ok"},{"id":"gone"},{"id":"busy"}]}`)
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch body.Model {
		case "ok":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`)
		case "gone":
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":{"message":"model not found"}}`)
		default:
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":"slow down"}}`)
		}
	}))
}

// TestProviderProbeAPI 端到端：目录静态种子三模型 → POST probe（master）
// → 三态结论齐；scoped token 403；未知 provider 400。
func TestProviderProbeAPI(t *testing.T) {
	up := newProbeUpstream(t)
	defer up.Close()

	adapter, err := openai_compat.New(openai_compat.Spec{
		ProviderName: "mockprobe", BaseURL: up.URL + "/v1", APIKey: "k",
	})
	if err != nil {
		t.Fatal(err)
	}
	provs := []provider.Provider{adapter}
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	s.SetRouter(&routing.Router{Providers: provs})
	cat := routing.NewCatalog(provs, nil, nil, nil, nil)
	cat.Sync(context.Background()) // 目录就位
	if n := len(cat.Snapshot()); n != 3 {
		t.Fatalf("catalog snapshot has %d models, want 3", n)
	}
	s.SetCatalog(cat)

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// scoped token（合法但仅 health 域）被 handler 403——探测是
	// master 专属；错误 token 在 scopeGuard 层已 401。
	scoped := DeriveMCPToken(testGatewayToken, []string{ScopeHealth})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/dashboard/api/provider/probe?provider=mockprobe&key="+scoped, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("scoped token status = %d, want 403", resp.StatusCode)
	}

	// master 探测：三模型三态。
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/dashboard/api/provider/probe?provider=mockprobe&key="+testGatewayToken, nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Results []probeResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	verdicts := map[string]string{}
	for _, r := range body.Results {
		verdicts[r.Model] = r.State
	}
	if verdicts["ok"] != ProbeAvailable {
		t.Errorf("ok = %s, want available", verdicts["ok"])
	}
	if verdicts["gone"] != ProbeUnavailable {
		t.Errorf("gone = %s, want unavailable", verdicts["gone"])
	}
	if verdicts["busy"] != ProbeThrottled {
		t.Errorf("busy = %s, want throttled", verdicts["busy"])
	}

	// GET 读缓存：同一批结论可恢复（页面刷新徽标恢复路径）。
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/dashboard/api/provider/probe?provider=mockprobe&key="+testGatewayToken, nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cached GET status = %d", resp.StatusCode)
	}
	var cached struct {
		Results []probeResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cached); err != nil {
		t.Fatal(err)
	}
	if len(cached.Results) != 3 {
		t.Fatalf("cached results = %d, want 3", len(cached.Results))
	}

	// 未知 provider 400。
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/dashboard/api/provider/probe?provider=nope&key="+testGatewayToken, nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown provider status = %d, want 400", resp.StatusCode)
	}
}
