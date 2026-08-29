package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/anthropic"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
)

// anthropicUpstreamBody 是 Anthropic Messages 上游的非流式响应样本。
const anthropicUpstreamBody = `{"id":"msg_1","type":"message","role":"assistant",` +
	`"model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",` +
	`"usage":{"input_tokens":2,"output_tokens":1}}`

// openaiUpstreamBody 是 openai_compat 上游的非流式响应样本。
const openaiUpstreamBody = `{"id":"c1","object":"chat.completion","created":1,"model":"m",` +
	`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
	`"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`

// stringCapture 捕获上游请求体（并发安全）。
type stringCapture struct {
	mu sync.Mutex
	b  strings.Builder
}

func (c *stringCapture) write(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.b.WriteString(s)
}

func (c *stringCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.String()
}

// withProviders 是测试装配捷径：SetRouter 后返回原 Server（链式）。
func (s *Server) withProviders(t *testing.T, ups ...provider.Provider) *Server {
	t.Helper()
	s.SetRouter(&routing.Router{Providers: ups})
	return s
}

// TestChatResponseFormatDegradedHeader：OpenAI 入站带 response_format
// 打到 Anthropic 上游（无原生结构化输出）→ 200 + 显式降级头，上游
// wire 不携带该字段（M3.6 验收：不支持的上游显式降级标记）。
func TestChatResponseFormatDegradedHeader(t *testing.T) {
	cap := &stringCapture{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cap.write(string(b))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, anthropicUpstreamBody)
	}))
	t.Cleanup(up.Close)

	ant, err := anthropic.New(anthropic.Spec{ProviderName: "ant", BaseURL: up.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("anthropic.New: %v", err)
	}
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)).withProviders(t, ant)
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)

	resp := postAuthed(t, gw.URL+"/v1/chat/completions",
		`{"model":"m","messages":[{"role":"user","content":"hi"}],`+
			`"response_format":{"type":"json_object"}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-OmniFusion-Degraded"); got != "response_format" {
		t.Errorf("degraded header = %q, want response_format", got)
	}
	if body := cap.String(); strings.Contains(body, "response_format") {
		t.Errorf("upstream wire leaked response_format: %s", body)
	}
}

// TestChatResponseFormatPassthrough：openai_compat 上游无损透传 →
// 无降级头，上游 wire 原样收到 response_format。
func TestChatResponseFormatPassthrough(t *testing.T) {
	cap := &stringCapture{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cap.write(string(b))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, openaiUpstreamBody)
	}))
	t.Cleanup(up.Close)

	oc, err := openai_compat.New(openai_compat.Spec{ProviderName: "oc", BaseURL: up.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("openai_compat.New: %v", err)
	}
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)).withProviders(t, oc)
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)

	resp := postAuthed(t, gw.URL+"/v1/chat/completions",
		`{"model":"m","messages":[{"role":"user","content":"hi"}],`+
			`"response_format":{"type":"json_object"}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-OmniFusion-Degraded"); got != "" {
		t.Errorf("degraded header = %q, want empty", got)
	}
	if body := cap.String(); !strings.Contains(body, `"response_format":{"type":"json_object"}`) {
		t.Errorf("upstream wire missing response_format: %s", body)
	}
}

// TestGeminiInboundStructuredToAnthropic：Gemini 入站的 responseSchema
// 归一为 IR response_format，跨协议打到 Anthropic 上游时显式降级。
func TestGeminiInboundStructuredToAnthropic(t *testing.T) {
	cap := &stringCapture{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cap.write(string(b))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, anthropicUpstreamBody)
	}))
	t.Cleanup(up.Close)

	ant, err := anthropic.New(anthropic.Spec{ProviderName: "ant", BaseURL: up.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("anthropic.New: %v", err)
	}
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)).withProviders(t, ant)
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)

	resp := postGemini(t, gw.URL+"/v1beta/models/m:generateContent",
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],`+
			`"generationConfig":{"responseMimeType":"application/json",`+
			`"responseSchema":{"type":"object","properties":{"a":{"type":"string"}}}}}`,
		testGatewayToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-OmniFusion-Degraded"); got != "response_format" {
		t.Errorf("degraded header = %q, want response_format", got)
	}
	if body := cap.String(); strings.Contains(body, "responseSchema") {
		t.Errorf("anthropic wire leaked schema: %s", body)
	}
}
