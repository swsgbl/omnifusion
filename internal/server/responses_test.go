package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
)

// responsesUpstreamPayload 捕获上游收到的 chat 请求体（翻译验证）。
type responsesUpstreamPayload struct {
	mu      sync.Mutex
	payload map[string]any
}

func (u *responsesUpstreamPayload) set(p map[string]any) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.payload = p
}

func (u *responsesUpstreamPayload) get() map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.payload
}

// newResponsesFixture 装配 /v1/responses 测试网关：上游按 stream 开关回
// OpenAI Chat 形响应（跨协议：Responses 入站 → Chat 上游），并捕获上游
// 请求体供翻译断言。
func newResponsesFixture(t *testing.T, stream bool) (*httptest.Server, *responsesUpstreamPayload) {
	t.Helper()
	cap := &responsesUpstreamPayload{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		cap.set(p)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: "+`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"model-a","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}`+"\n\n")
			io.WriteString(w, "data: "+`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"model-a","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`+"\n\n")
			io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"model-a",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	t.Cleanup(up.Close)

	a, err := openai_compat.New(openai_compat.Spec{ProviderName: "a", BaseURL: up.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	s.SetRouter(&routing.Router{Providers: []provider.Provider{a}})
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	return gw, cap
}

const responsesBody = `{"model":"gpt-x","instructions":"be brief","input":[
	{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
	{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"ls\"}","call_id":"call_1"},
	{"type":"function_call_output","call_id":"call_1","output":"a b"}
]}`

func TestResponsesRejectsBadAuth(t *testing.T) {
	gw, _ := newResponsesFixture(t, false)
	resp := postBare(t, gw.URL+"/v1/responses", responsesBody)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bare status = %d, want 401", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "authentication_error") {
		t.Fatalf("body = %s, want openai error shape", b)
	}
}

func TestResponsesNonStreamRoundTrip(t *testing.T) {
	gw, cap := newResponsesFixture(t, false)
	resp := postAuthed(t, gw.URL+"/v1/responses", responsesBody)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, b)
	}
	var out struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Status  string `json:"status"`
		Output  []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(out.ID, "resp_") || out.Object != "response" || out.Status != "completed" {
		t.Fatalf("head = %s/%s/%s", out.ID, out.Object, out.Status)
	}
	if len(out.Output) != 1 || out.Output[0].Type != "message" || out.Output[0].Role != "assistant" ||
		len(out.Output[0].Content) != 1 || out.Output[0].Content[0].Text != "ok" {
		t.Fatalf("output = %+v", out.Output)
	}
	if out.Usage.InputTokens != 3 || out.Usage.OutputTokens != 2 || out.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %+v", out.Usage)
	}

	up := cap.get() // 上游收到的是翻译后的 Chat 形
	msgs, _ := up["messages"].([]any)
	if len(msgs) != 4 { // system + user + assistant(tool_call) + tool
		t.Fatalf("upstream messages = %d, want 4: %v", len(msgs), up["messages"])
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "be brief" {
		t.Fatalf("first upstream message = %v", first)
	}
	last, _ := msgs[3].(map[string]any)
	if last["role"] != "tool" || last["tool_call_id"] != "call_1" {
		t.Fatalf("last upstream message = %v", last)
	}
}

func TestResponsesStreamSequence(t *testing.T) {
	gw, _ := newResponsesFixture(t, true)
	resp := postAuthed(t, gw.URL+"/v1/responses",
		`{"model":"gpt-x","input":"hi","stream":true}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %s", ct)
	}
	b, _ := io.ReadAll(resp.Body)
	s := string(b)
	for _, ev := range []string{
		"event: response.created\n",
		"event: response.output_item.added\n",
		"event: response.content_part.added\n",
		"event: response.output_text.delta\n",
		"event: response.output_text.done\n",
		"event: response.completed\n",
	} {
		if !strings.Contains(s, ev) {
			t.Fatalf("event %q missing:\n%s", ev, s)
		}
	}
	if !strings.Contains(s, `"delta":"Hel"`) || !strings.Contains(s, `"text":"Hel"`) {
		t.Fatalf("delta/full text missing:\n%s", s)
	}
}

func TestResponsesDegradedHeaderAndValidation(t *testing.T) {
	gw, _ := newResponsesFixture(t, false)
	resp := postAuthed(t, gw.URL+"/v1/responses",
		`{"model":"gpt-x","input":"hi","reasoning":{"effort":"low"}}`)
	defer resp.Body.Close()
	if resp.Header.Get("X-OmniFusion-Degraded") != "reasoning" {
		t.Fatalf("degraded = %q, want reasoning", resp.Header.Get("X-OmniFusion-Degraded"))
	}

	resp = postAuthed(t, gw.URL+"/v1/responses", `{"input":"no model"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("model-missing status = %d, want 400", resp.StatusCode)
	}

	resp = postAuthed(t, gw.URL+"/v1/responses", `{"model":"gpt-x"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty-input status = %d, want 400", resp.StatusCode)
	}
}

func TestResponsesStringToolChoiceWire(t *testing.T) { // tool_choice="auto" 直通上游
	gw, cap := newResponsesFixture(t, false)
	resp := postAuthed(t, gw.URL+"/v1/responses",
		`{"model":"gpt-x","input":"hi","tool_choice":"auto"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if cap.get()["tool_choice"] != "auto" {
		t.Fatalf("upstream tool_choice = %v", cap.get()["tool_choice"])
	}
}
