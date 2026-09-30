// genai_e2e_test.go 锁死 GenAI 关联行接线（蓝图 Phase 9）：/v1/chat
// 成功路径输出 semconv 命名字段（operation/system/request.model/
// response.model + ok=true）；A2A 流任务终态行带 task.id 与
// conversation.id；载荷永不出现（关联面只有 ID/名称/枚举）。
package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/swsgbl/omnifusion/internal/a2a"
	"github.com/swsgbl/omnifusion/internal/agent"
	"github.com/swsgbl/omnifusion/internal/obs"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/store"
)

func TestGenAICorrelationOnChat(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"model-a","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer up.Close()

	var mu sync.Mutex
	buf := &strings.Builder{}
	log := slog.New(slog.NewJSONHandler(&lockedWriter{mu: &mu, dst: buf}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	a, _ := openai_compat.New(openai_compat.Spec{ProviderName: "mock", BaseURL: up.URL + "/v1", APIKey: "k"})
	st, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer func() { _ = st.Close() }()
	s := authedServer(New(nil, log, st))
	s.SetRouter(&routing.Router{Providers: []provider.Provider{a}})
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()

	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"model-a","messages":[{"role":"user","content":"secret-free-prompt"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testGatewayToken)
	req.Header.Set("X-Request-Id", "req-genai-1")
	req.Header.Set(routing.HeaderSession, "sess-genai-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	mu.Lock()
	logged := buf.String()
	mu.Unlock()
	if !strings.Contains(logged, "genai operation") {
		t.Fatalf("genai correlation line missing:\n%s", logged)
	}
	for _, want := range []string{
		obs.FieldOperation, obs.OpChatCompletions,
		obs.FieldSystem, "mock",
		obs.FieldRequestModel, "model-a",
		obs.FieldRequestID, "req-genai-1",
		obs.FieldConversationID, "sess-genai-1",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log missing %q:\n%s", want, logged)
		}
	}
	// 关联面纪律：请求载荷不得出现在日志里。
	if strings.Contains(logged, "secret-free-prompt") {
		t.Fatalf("payload leaked into correlation line:\n%s", logged)
	}
}

func TestGenAICorrelationOnA2AStreamTask(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, streamUpstreamBody())
	}))
	defer up.Close()

	var mu sync.Mutex
	buf := &strings.Builder{}
	log := slog.New(slog.NewJSONHandler(&lockedWriter{mu: &mu, dst: buf}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	a, _ := openai_compat.New(openai_compat.Spec{ProviderName: "mock", BaseURL: up.URL + "/v1", APIKey: "k"})
	st, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer func() { _ = st.Close() }()
	s := authedServer(New(nil, log, st))
	s.SetRouter(&routing.Router{Providers: []provider.Provider{a}})
	s.SetA2A(a2a.BuildCard(a2a.CardOptions{BaseURL: "http://gw.test", Version: "t", DefaultModel: "m", Streaming: true}), "m")
	s.SetA2ATasks(agent.NewTaskStore())
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()

	taskID, frames := streamA2A(t, gw.URL)
	if len(frames) < 3 {
		t.Fatalf("frames = %d", len(frames))
	}

	mu.Lock()
	logged := buf.String()
	mu.Unlock()
	if !strings.Contains(logged, obs.OpA2AStream) {
		t.Fatalf("a2a stream correlation missing:\n%s", logged)
	}
	if !strings.Contains(logged, taskID) {
		t.Fatalf("task id %s missing from correlation:\n%s", taskID, logged)
	}
	if !strings.Contains(logged, `"ok":true`) {
		t.Fatalf("completed stream must log ok=true:\n%s", logged)
	}
}

// 编译期锁定操作名集合（改名=破坏日志消费方）。
func TestGenAIOperationNames(t *testing.T) {
	want := map[string]string{
		obs.OpChatCompletions: "chat.completions",
		obs.OpA2ASend:         "a2a.send_message",
		obs.OpA2AStream:       "a2a.send_streaming_message",
		obs.OpA2ATaskGet:      "a2a.get_task",
	}
	for k, v := range want {
		if k != v {
			t.Fatalf("operation name %q != %q", k, v)
		}
	}
}
