// decisions_e2e_test.go 锁死回放记录接线（蓝图 Phase 9 replay
// record）：/v1/chat 请求后 route_decisions 表出现一行——endpoint/
// success/原因码/候选集证据齐全，且用户载荷构造性不出现在 evidence。
package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/store"
)

func TestRouteDecisionReplayRecordOnChat(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"model-a","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer up.Close()

	a, _ := openai_compat.New(openai_compat.Spec{ProviderName: "mock", BaseURL: up.URL + "/v1", APIKey: "k"})
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), st))
	s.SetRouter(&routing.Router{Providers: []provider.Provider{a}})
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()

	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"model-a","messages":[{"role":"user","content":"replay-secret-prompt"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testGatewayToken)
	req.Header.Set("X-Request-Id", "req-replay-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	rows, err := st.LoadRecentRouteDecisions(10)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("route_decisions rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.Endpoint != "chat" || !r.Success || r.ChosenProvider != "mock" || r.RequestID != "req-replay-1" {
		t.Fatalf("row = %+v", r)
	}
	for _, want := range []string{"FIRST_SUCCESS", "candidates", "model-a"} {
		if !strings.Contains(r.Evidence, want) {
			t.Fatalf("evidence missing %q: %s", want, r.Evidence)
		}
	}
	// 最小证据集纪律：用户载荷不落 evidence。
	if strings.Contains(r.Evidence, "replay-secret-prompt") {
		t.Fatalf("user payload leaked into evidence: %s", r.Evidence)
	}
}
