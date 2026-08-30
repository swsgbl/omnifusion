package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
)

// newStrategyFixture 装配双上游路由：a 快且健康但配额半满，
// b 慢但配额未设限——auto/fast 选 a，cheap 选 b，可互相区分。
func newStrategyFixture(t *testing.T) (*Server, *httptest.Server, *string) {
	t.Helper()
	var gotModel string
	mkUpstream := func(model string, capture *string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if capture != nil {
				b, _ := io.ReadAll(r.Body)
				*capture = string(b)
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{
				"id":"c1","object":"chat.completion","created":1,"model":"`+model+`",
				"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
			}`)
		}))
	}
	upA := mkUpstream("model-a", nil)
	t.Cleanup(upA.Close)
	upB := mkUpstream("model-b", &gotModel)
	t.Cleanup(upB.Close)

	a, err := openai_compat.New(openai_compat.Spec{ProviderName: "a", BaseURL: upA.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("adapter a: %v", err)
	}
	b, err := openai_compat.New(openai_compat.Spec{ProviderName: "b", BaseURL: upB.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("adapter b: %v", err)
	}

	scorer := routing.NewScorer()
	scorer.Observe("a", 10*time.Millisecond, true) // a：快且健康
	scorer.Observe("b", 3*time.Second, true)       // b：慢
	quota := routing.NewQuotaTracker()
	quota.SetLimit("a", routing.QuotaLimits{RPM: 10})
	quota.RecordRequest("a") // a：余量 9/10（够多条分发不触硬阻断）

	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	s.SetRouter(&routing.Router{
		Providers: []provider.Provider{a, b},
		Scoring:   scorer,
		Quota:     quota,
	})
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	return s, gw, &gotModel
}

func postWithStrategy(t *testing.T, url, body, header string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testGatewayToken)
	if header != "" {
		req.Header.Set(routing.HeaderStrategy, header)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}

func servedModel(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, b)
	}
	var out struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.Model
}

// TestStrategySelectViaModelDirective 是 验收之一：策略可经
// model 内嵌 @指令 选择，且上游收到的是重写后的裸模型名。
func TestStrategySelectViaModelDirective(t *testing.T) {
	_, gw, gotModel := newStrategyFixture(t)

	// cheap → b（配额余量优先，压过延迟劣势）
	resp := postWithStrategy(t, gw.URL+"/v1/chat/completions",
		`{"model":"@cheap:target-model","messages":[{"role":"user","content":"hi"}]}`, "")
	if m := servedModel(t, resp); m != "model-b" {
		t.Fatalf("cheap directive served by %q, want model-b", m)
	}
	if strings.Contains(*gotModel, "@") {
		t.Fatalf("upstream saw raw directive in body: %s", *gotModel)
	}
	if !strings.Contains(*gotModel, "target-model") {
		t.Fatalf("upstream body missing bare model: %s", *gotModel)
	}
}

// TestStrategySelectViaHeader 是 验收之二：策略可经 header 选择。
func TestStrategySelectViaHeader(t *testing.T) {
	_, gw, _ := newStrategyFixture(t)

	// fast → a（延迟优先）
	resp := postWithStrategy(t, gw.URL+"/v1/chat/completions",
		`{"model":"target-model","messages":[{"role":"user","content":"hi"}]}`, "fast")
	if m := servedModel(t, resp); m != "model-a" {
		t.Fatalf("fast header served by %q, want model-a", m)
	}

	// 默认 auto → a（健康+延迟加权打分同样选 a）
	resp = postWithStrategy(t, gw.URL+"/v1/chat/completions",
		`{"model":"target-model","messages":[{"role":"user","content":"hi"}]}`, "")
	if m := servedModel(t, resp); m != "model-a" {
		t.Fatalf("auto served by %q, want model-a", m)
	}
}

// TestStrategyRejectsBadInput 非法策略名与缺目标模型都回 400。
func TestStrategyRejectsBadInput(t *testing.T) {
	_, gw, _ := newStrategyFixture(t)

	resp := postWithStrategy(t, gw.URL+"/v1/chat/completions",
		`{"model":"@bogus:target-model","messages":[{"role":"user","content":"hi"}]}`, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown strategy status = %d, want 400", resp.StatusCode)
	}

	resp = postWithStrategy(t, gw.URL+"/v1/chat/completions",
		`{"model":"@cheap","messages":[{"role":"user","content":"hi"}]}`, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("directive without model status = %d, want 400", resp.StatusCode)
	}

	resp = postWithStrategy(t, gw.URL+"/v1/chat/completions",
		`{"model":"target-model","messages":[{"role":"user","content":"hi"}]}`, "bogus")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bogus header status = %d, want 400", resp.StatusCode)
	}
}
