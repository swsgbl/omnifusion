package server

import (
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

// newSessionFixture 装配启用 sticky session 的双上游网关。初始无观测
// （tie → 注册序 a）；测试中途把 a 观测成慢速让策略序偏向 b——与
// s1→a 的绑定形成对冲，可区分"sticky 压过策略序"与"策略序接管"。
func newSessionFixture(t *testing.T) (*routing.Scorer, *httptest.Server) {
	t.Helper()
	mkUpstream := func(model string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{
				"id":"c1","object":"chat.completion","created":1,"model":"`+model+`",
				"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
			}`)
		}))
	}
	upA := mkUpstream("model-a")
	t.Cleanup(upA.Close)
	upB := mkUpstream("model-b")
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
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	s.SetRouter(&routing.Router{
		Providers: []provider.Provider{a, b},
		Scoring:   scorer,
		Sessions:  routing.NewSessionTracker(),
	})
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	return scorer, gw
}

// postWithSession 发一条带可选会话头的 chat 请求。
func postWithSession(t *testing.T, url, body, session string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testGatewayToken)
	if session != "" {
		req.Header.Set(routing.HeaderSession, session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}

// TestSessionStickinessViaHeader 是 M2.7 验收：X-Session-Id 从 HTTP
// 边界流到路由——同会话粘住首次命中的 provider，压过后来的策略序；
// 无头的请求仍按策略序走。
func TestSessionStickinessViaHeader(t *testing.T) {
	scorer, gw := newSessionFixture(t)
	body := `{"model":"target-model","messages":[{"role":"user","content":"hi"}]}`

	// 1. 无观测 tie → 注册序 a；s1 绑定 a
	if m := servedModel(t, postWithSession(t, gw.URL+"/v1/chat/completions", body, "s1")); m != "model-a" {
		t.Fatalf("post 1 served by %q, want model-a", m)
	}

	// 2. a 观测成慢速：策略序偏 b，但同会话仍粘 a
	scorer.Observe("a", 3*time.Second, true)
	if m := servedModel(t, postWithSession(t, gw.URL+"/v1/chat/completions", body, "s1")); m != "model-a" {
		t.Fatalf("post 2 (same session) served by %q, want model-a (sticky)", m)
	}

	// 3. 无会话头：回到策略序 → b
	if m := servedModel(t, postWithSession(t, gw.URL+"/v1/chat/completions", body, "")); m != "model-b" {
		t.Fatalf("post 3 (no session) served by %q, want model-b", m)
	}
}
