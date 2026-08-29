package server

import (
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

// newMessagesFixture 装配 /v1/messages 测试网关：上游按 stream 开关回
// OpenAI 形非流式/流式响应，入站经 Anthropic 翻译往返（M3.1 验收路径）。
func newMessagesFixture(t *testing.T, stream bool) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	return gw
}

// postMessages 以 x-api-key（Claude Code ANTHROPIC_API_KEY 形）发请求。
func postMessages(t *testing.T, url, body, key string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("x-api-key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}

const anthropicBody = `{"model":"claude-x","max_tokens":64,` +
	`"system":"be brief","messages":[{"role":"user","content":"hi"}]}`

func TestMessagesRejectsBadAuth(t *testing.T) {
	gw := newMessagesFixture(t, false)

	resp := postBare(t, gw.URL+"/v1/messages", anthropicBody)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bare status = %d, want 401", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `"authentication_error"`) {
		t.Fatalf("body = %s, want anthropic error shape", b)
	}

	resp = postMessages(t, gw.URL+"/v1/messages", anthropicBody, "ofg-wrong")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong key status = %d, want 401", resp.StatusCode)
	}
}

func TestMessagesNonStreamRoundTrip(t *testing.T) {
	gw := newMessagesFixture(t, false)

	resp := postMessages(t, gw.URL+"/v1/messages", anthropicBody, testGatewayToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, b)
	}
	var out struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(out.ID, "msg_") || out.Type != "message" || out.Role != "assistant" {
		t.Fatalf("head = %s/%s/%s", out.ID, out.Type, out.Role)
	}
	if len(out.Content) != 1 || out.Content[0].Type != "text" || out.Content[0].Text != "ok" {
		t.Fatalf("content = %+v, want [{text ok}]", out.Content)
	}
	if out.StopReason != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn", out.StopReason)
	}
	if out.Usage.InputTokens != 3 || out.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v, want 3/2", out.Usage)
	}
}

func TestMessagesBearerAuthAlsoWorks(t *testing.T) { // ANTHROPIC_AUTH_TOKEN 形
	gw := newMessagesFixture(t, false)
	resp := postAuthed(t, gw.URL+"/v1/messages", anthropicBody)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bearer status = %d, want 200", resp.StatusCode)
	}
}

func TestMessagesValidatesRequiredFields(t *testing.T) {
	gw := newMessagesFixture(t, false)

	for name, body := range map[string]string{
		"missing max_tokens": `{"model":"m","messages":[{"role":"user","content":"x"}]}`,
		"missing messages":   `{"model":"m","max_tokens":8}`,
		"missing model":      `{"max_tokens":8,"messages":[{"role":"user","content":"x"}]}`,
		"invalid json":       `{"model":`,
	} {
		resp := postMessages(t, gw.URL+"/v1/messages", body, testGatewayToken)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, body = %s", name, resp.StatusCode, b)
		}
		if !strings.Contains(string(b), "invalid_request_error") {
			t.Errorf("%s: body = %s, want anthropic error shape", name, b)
		}
	}
}

func TestMessagesDegradedHeader(t *testing.T) {
	gw := newMessagesFixture(t, false)
	body := `{"model":"claude-x","max_tokens":64,"top_k":40,` +
		`"messages":[{"role":"user","content":"hi"}]}`

	resp := postMessages(t, gw.URL+"/v1/messages", body, testGatewayToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-OmniFusion-Degraded"); got != "top_k" {
		t.Fatalf("degraded header = %q, want top_k", got)
	}
}

func TestMessagesStreamSequence(t *testing.T) {
	gw := newMessagesFixture(t, true)
	body := `{"model":"claude-x","max_tokens":64,` +
		`"messages":[{"role":"user","content":"hi"}],"stream":true}`

	resp := postMessages(t, gw.URL+"/v1/messages", body, testGatewayToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	b, _ := io.ReadAll(resp.Body)
	out := string(b)

	for _, want := range []string{
		"event: message_start", "event: content_block_start", "event: ping",
		"event: content_block_delta", `"text":"Hel"`,
		"event: content_block_stop", "event: message_delta",
		`"stop_reason":"end_turn"`, "event: message_stop",
		`"output_tokens":2`, // 末块 usage 权威值
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stream missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "[DONE]") {
		t.Errorf("anthropic stream must not end with OpenAI [DONE]")
	}
}

// TestMessagesStreamMidStreamBreak 验收 M3.4：首帧落地后上游断流，
// Anthropic 入站侧仍以 message_delta + message_stop 完整收尾，
// 客户端拿到优雅结束而非悬挂连接。
func TestMessagesStreamMidStreamBreak(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: "+`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"model-a","choices":[{"index":0,"delta":{"content":"par"},"finish_reason":null}]}`+"\n\n")
	}))
	t.Cleanup(broken.Close)

	a, err := openai_compat.New(openai_compat.Spec{ProviderName: "broken", BaseURL: broken.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	s.SetRouter(&routing.Router{Providers: []provider.Provider{a}})
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)

	body := `{"model":"claude-x","max_tokens":64,` +
		`"messages":[{"role":"user","content":"hi"}],"stream":true}`
	resp := postMessages(t, gw.URL+"/v1/messages", body, testGatewayToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, b)
	}
	b, _ := io.ReadAll(resp.Body)
	out := string(b)
	for _, want := range []string{
		"event: message_start",
		`"text":"par"`,
		"event: message_delta",
		`"stop_reason":"end_turn"`, // 未见 finish 的断流按 end_turn 收尾
		"event: message_stop",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stream missing %q\n%s", want, out)
		}
	}
}
