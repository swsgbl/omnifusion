package obs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestMetricsNilSafe：未装配（nil）时全方法 no-op、Handler 走 404——
// server 侧不需要判空即可调用。
func TestMetricsNilSafe(t *testing.T) {
	var m *Metrics
	m.RecordRequest("chat", "groq", 200, time.Second)
	m.RecordTTFT("chat", "groq", 100*time.Millisecond)
	m.RecordTokens("groq", 10, 20)
	m.RecordGuardrail("pii", "email", "block")
	m.RecordAttemptFailure("groq", "rate_limit")

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("nil Handler status = %d, want 404", rec.Code)
	}
}

// TestMetricsExposition：注册族齐全、标签组合与值正确落入文本协议，
// go/process 默认收集器在位（Grafana 通用面板依赖）。
func TestMetricsExposition(t *testing.T) {
	m := NewMetrics()
	m.RecordRequest("chat", "groq", 200, 150*time.Millisecond)
	m.RecordRequest("messages", "cache", 200, 3*time.Millisecond)
	m.RecordTTFT("chat", "groq", 120*time.Millisecond)
	m.RecordTokens("groq", 11, 7)
	m.RecordGuardrail("pii", "email", "block")
	m.RecordGuardrail("pii", "email", "block")
	m.RecordAttemptFailure("groq", "rate_limit")

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content-type = %q, want text/plain", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`omnifusion_requests_total{endpoint="chat",provider="groq",status="200"} 1`,
		`omnifusion_requests_total{endpoint="messages",provider="cache",status="200"} 1`,
		`omnifusion_request_duration_seconds_bucket{endpoint="chat",provider="groq",le="0.25"} 1`,
		`omnifusion_ttft_seconds_bucket{endpoint="chat",provider="groq",le="0.25"} 1`,
		`omnifusion_tokens_total{direction="prompt",provider="groq"} 11`,
		`omnifusion_tokens_total{direction="completion",provider="groq"} 7`,
		`omnifusion_guardrails_findings_total{action="block",kind="pii",rule="email"} 2`,
		`omnifusion_upstream_failures_total{kind="rate_limit",provider="groq"} 1`,
		// go/process 收集器在位
		"go_goroutines",
		"process_start_time_seconds",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition missing %q", want)
		}
	}
}

// TestMetricsZeroTokens：0 token 不产生样本（避免空 provider 行噪声）。
func TestMetricsZeroTokens(t *testing.T) {
	m := NewMetrics()
	m.RecordTokens("groq", 0, 0)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if strings.Contains(rec.Body.String(), "omnifusion_tokens_total{") {
		t.Error("zero-token record should emit no sample")
	}
}
