// audit_test.go 是 验收（数据面出账）：非流式/流式/失败/护栏拦截
// 四类出口各落一行审计 + 指标双写；audit.enabled=false 只计指标不落库；
// /metrics 端点鉴权与暴露。
package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/config"
	"github.com/swsgbl/omnifusion/internal/obs"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/security"
	"github.com/swsgbl/omnifusion/internal/store"
)

// auditFixture 聚合被测面：网关 URL、真 store、指标、上游命中数。
type auditFixture struct {
	gw      *httptest.Server
	st      *store.Store
	metrics *obs.Metrics
	hit     *int64
}

// newAuditFixture 装配审计测试网关：真 SQLite store + 指标 + mock 上游
// （非流式回 JSON usage，流式回 SSE 含尾帧 usage）。mode 控制附加装配。
func newAuditFixture(t *testing.T, auditOn bool, guard *security.Guardrails) *auditFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var n int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 本机单调时钟 ~515µs/tick：不加延迟时本地往返可能 <1 tick，
		// latency_ms 记 0 而「>0」断言环境性抖动失败。固定跨 tick。
		time.Sleep(2 * time.Millisecond)
		atomic.AddInt64(&n, 1)
		if strings.Contains(r.URL.Path, "chat") && r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			var probe struct {
				Stream bool `json:"stream"`
			}
			_ = json.Unmarshal(b, &probe)
			if probe.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,"+
					"\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hi\"},\"finish_reason\":null}]}\n\n"+
					"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,"+
					"\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],"+
					"\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3,\"total_tokens\":8}}\n\n"+
					"data: [DONE]\n\n")
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"m",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
	}))
	t.Cleanup(upstream.Close)

	adapter, err := openai_compat.New(openai_compat.Spec{
		ProviderName: "mock", BaseURL: upstream.URL + "/v1", APIKey: "sk-test",
	})
	if err != nil {
		t.Fatalf("openai_compat.New: %v", err)
	}

	cfg := &config.Config{Audit: config.AuditConfig{Enabled: auditOn, MaxRows: 10000}}
	s := authedServer(New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), st))
	m := obs.NewMetrics()
	s.SetMetrics(m)
	s.SetRouter(&routing.Router{Providers: []provider.Provider{adapter}})
	if guard != nil {
		s.SetGuardrails(guard)
	}
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	return &auditFixture{gw: gw, st: st, metrics: m, hit: &n}
}

// rows 读审计行（最新在前）。
func rows(t *testing.T, f *auditFixture) []store.RequestLogID {
	t.Helper()
	got, err := f.st.QueryRequestLogs(store.RequestLogFilter{})
	if err != nil {
		t.Fatalf("query request_log: %v", err)
	}
	return got
}

func exposition(t *testing.T, f *auditFixture) string {
	t.Helper()
	rec := httptest.NewRecorder()
	f.metrics.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}

func TestAuditNonStreamSuccess(t *testing.T) {
	f := newAuditFixture(t, true, nil)
	resp := postAuthed(t, f.gw.URL+"/v1/chat/completions",
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	got := rows(t, f)
	if len(got) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(got))
	}
	r := got[0]
	if r.Endpoint != "chat" || r.Provider != "mock" || r.Status != 200 ||
		r.TokensIn != 4 || r.TokensOut != 2 || r.TTFTMS >= 0 || r.CacheHit {
		t.Errorf("row = %+v", r)
	}
	if r.LatencyMS <= 0 {
		t.Errorf("latency_ms = %v, want > 0", r.LatencyMS)
	}
	exp := exposition(t, f)
	for _, want := range []string{
		`omnifusion_requests_total{endpoint="chat",provider="mock",status="200"} 1`,
		`omnifusion_tokens_total{direction="prompt",provider="mock"} 4`,
	} {
		if !strings.Contains(exp, want) {
			t.Errorf("exposition missing %q", want)
		}
	}
}

func TestAuditStreamTTFTAndUsage(t *testing.T) {
	f := newAuditFixture(t, true, nil)
	resp := postAuthed(t, f.gw.URL+"/v1/chat/completions",
		`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	io.Copy(io.Discard, resp.Body) // 读尽流触发收尾出账

	got := rows(t, f)
	if len(got) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(got))
	}
	r := got[0]
	if r.Provider != "mock" || r.Status != 200 || r.TokensIn != 5 || r.TokensOut != 3 {
		t.Errorf("row = %+v", r)
	}
	if r.TTFTMS < 0 {
		t.Errorf("stream row ttft_ms = %v, want >= 0", r.TTFTMS)
	}
	if !strings.Contains(exposition(t, f), "omnifusion_ttft_seconds_bucket") {
		t.Error("exposition missing ttft samples")
	}
}

func TestAuditDispatchFailure(t *testing.T) {
	// 独立 500 上游：单候选失败 → 502，逐 attempt 失败进指标。
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "boom"})
	}))
	t.Cleanup(broken.Close)
	st, err := store.Open(filepath.Join(t.TempDir(), "fail.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	adapter, err := openai_compat.New(openai_compat.Spec{
		ProviderName: "broken", BaseURL: broken.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	cfg := &config.Config{Audit: config.AuditConfig{Enabled: true, MaxRows: 10000}}
	s := authedServer(New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), st))
	m := obs.NewMetrics()
	s.SetMetrics(m)
	s.SetRouter(&routing.Router{Providers: []provider.Provider{adapter}})
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)

	resp := postAuthed(t, gw.URL+"/v1/chat/completions",
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	got, err := st.QueryRequestLogs(store.RequestLogFilter{})
	if err != nil || len(got) != 1 {
		t.Fatalf("rows err %v, n = %d, want 1", err, len(got))
	}
	r := got[0]
	if r.Status != 502 || r.ErrKind != "upstream_5xx" || r.Provider != "none" {
		t.Errorf("row = %+v", r)
	}
	if r.TTFTMS != -1 {
		t.Errorf("ttft_ms = %v, want -1 (non-stream failure)", r.TTFTMS)
	}
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rec.Body.String(),
		`omnifusion_upstream_failures_total{kind="upstream_5xx",provider="broken"} 1`) {
		t.Error("exposition missing upstream failure counter")
	}
}

func TestAuditGuardrailsBlock(t *testing.T) {
	f := newAuditFixture(t, true, mustGuard(t, security.GuardrailsOptions{}))
	resp := postAuthed(t, f.gw.URL+"/v1/chat/completions",
		`{"model":"m","messages":[{"role":"user","content":"邮箱 alice@example.com"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	got := rows(t, f)
	if len(got) != 1 || got[0].Status != 400 || got[0].ErrKind != "guardrails" ||
		got[0].Provider != "none" {
		t.Fatalf("row = %+v", got)
	}
	if !strings.Contains(exposition(t, f),
		`omnifusion_guardrails_findings_total{action="block",kind="pii",rule="email"} 1`) {
		t.Error("exposition missing guardrails finding counter")
	}
}

func TestAuditDisabledStillCountsMetrics(t *testing.T) {
	f := newAuditFixture(t, false, nil)
	resp := postAuthed(t, f.gw.URL+"/v1/chat/completions",
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if n, _ := f.st.CountRequestLogs(); n != 0 {
		t.Errorf("rows = %d, want 0 (audit disabled)", n)
	}
	if !strings.Contains(exposition(t, f), `provider="mock",status="200"} 1`) {
		t.Error("metrics must still be recorded when audit disabled")
	}
}

func TestMetricsEndpointAuth(t *testing.T) {
	f := newAuditFixture(t, true, nil)
	// 空族不输出样本：先打一个数据面请求再验证暴露。
	resp := postAuthed(t, f.gw.URL+"/v1/chat/completions",
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	resp.Body.Close()
	bare, err := http.Get(f.gw.URL + "/metrics")
	if err != nil {
		t.Fatalf("bare GET /metrics: %v", err)
	}
	defer bare.Body.Close()
	if bare.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bare /metrics = %d, want 401", bare.StatusCode)
	}
	req, err := http.NewRequest(http.MethodGet, f.gw.URL+"/metrics", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testGatewayToken)
	authed, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authed GET /metrics: %v", err)
	}
	defer authed.Body.Close()
	body, err := io.ReadAll(authed.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if authed.StatusCode != http.StatusOK || !strings.Contains(string(body), "omnifusion_") {
		t.Fatalf("authed /metrics = %d, body has metrics = %v", authed.StatusCode,
			strings.Contains(string(body), "omnifusion_"))
	}
}
