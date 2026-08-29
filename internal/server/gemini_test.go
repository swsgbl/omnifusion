// gemini_test.go 验收 Gemini 入站面（M3.2）：上游按 stream 开关回
// OpenAI 形响应，入站经 Gemini 翻译往返——与 messages_test.go 同构的
// 验收路径，外加路径拆分与 Google 形错误面的单测。
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

func newGeminiFixture(t *testing.T, stream bool) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: "+`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"model-a","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}`+"\n\n")
			io.WriteString(w, "data: "+`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"model-a","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`+"\n\n")
			io.WriteString(w, "data: "+`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"model-a","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`+"\n\n")
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

// postGemini 以 x-goog-api-key（Google SDK 形）发请求。
func postGemini(t *testing.T, url, body, key string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("x-goog-api-key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}

const geminiBody = `{"systemInstruction":{"parts":[{"text":"be brief"}],` +
	`"role":"user"},"contents":[{"role":"user","parts":[{"text":"hi"}]}],` +
	`"generationConfig":{"temperature":0.5,"maxOutputTokens":64,"topK":3}}`

func TestSplitGeminiPath(t *testing.T) {
	cases := []struct {
		path          string
		model, action string
		ok            bool
	}{
		{"/v1beta/models/gemini-x:generateContent", "gemini-x", "generateContent", true},
		{"/v1beta/models/gemini-x:streamGenerateContent", "gemini-x", "streamGenerateContent", true},
		{"/v1beta/models/gemini-x:countTokens", "", "", false},
		{"/v1beta/models/gemini-x", "", "", false},
		{"/v1beta/models/", "", "", false},
	}
	for _, c := range cases {
		model, action, ok := splitGeminiPath(c.path)
		if ok != c.ok || model != c.model || action != c.action {
			t.Errorf("split(%q) = %q,%q,%v; want %q,%q,%v",
				c.path, model, action, ok, c.model, c.action, c.ok)
		}
	}
}

func TestGeminiRejectsBadAuth(t *testing.T) {
	gw := newGeminiFixture(t, false)
	url := gw.URL + "/v1beta/models/gemini-x:generateContent"

	resp := postBare(t, url, geminiBody)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bare status = %d, want 401", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `"UNAUTHENTICATED"`) {
		t.Fatalf("body = %s, want google error shape", b)
	}

	resp = postGemini(t, url, geminiBody, "ofg-wrong")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong key status = %d, want 401", resp.StatusCode)
	}

	// ?key= 查询参数鉴权（分享链接/curl 形态）同样受支持。
	resp = postBare(t, url+"?key="+testGatewayToken, geminiBody)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("query key status = %d, want 200", resp.StatusCode)
	}
}

func TestGeminiUnknownActionIs404(t *testing.T) {
	gw := newGeminiFixture(t, false)
	resp := postGemini(t, gw.URL+"/v1beta/models/gemini-x:countTokens",
		geminiBody, testGatewayToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestGeminiNonStreamRoundTrip(t *testing.T) {
	gw := newGeminiFixture(t, false)
	resp := postGemini(t, gw.URL+"/v1beta/models/gemini-x:generateContent",
		geminiBody, testGatewayToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, b)
	}
	if got := resp.Header.Get("X-OmniFusion-Degraded"); !strings.Contains(got, "top_k") {
		t.Errorf("degraded = %q, want top_k surfaced (no silent drop)", got)
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Role  string `json:"role"`
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata *struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("body not gemini shape: %v (%s)", err, b)
	}
	if len(out.Candidates) != 1 ||
		len(out.Candidates[0].Content.Parts) != 1 ||
		out.Candidates[0].Content.Parts[0].Text != "ok" {
		t.Errorf("candidates = %+v", out.Candidates)
	}
	if out.Candidates[0].Content.Role != "model" {
		t.Errorf("role = %q, want model", out.Candidates[0].Content.Role)
	}
	if out.Candidates[0].FinishReason != "STOP" {
		t.Errorf("finishReason = %q, want STOP", out.Candidates[0].FinishReason)
	}
	if out.UsageMetadata == nil || out.UsageMetadata.PromptTokenCount != 3 {
		t.Errorf("usageMetadata = %+v", out.UsageMetadata)
	}
}

func TestGeminiStreamRoundTrip(t *testing.T) {
	gw := newGeminiFixture(t, true)
	resp := postGemini(t, gw.URL+"/v1beta/models/gemini-x:streamGenerateContent",
		geminiBody, testGatewayToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, b)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	if !strings.Contains(body, `"text":"Hel"`) || !strings.Contains(body, `"text":"lo"`) {
		t.Errorf("delta frames missing: %s", body)
	}
	if !strings.Contains(body, `"finishReason":"STOP"`) {
		t.Errorf("finishReason frame missing: %s", body)
	}
	if !strings.Contains(body, `"promptTokenCount":4`) ||
		!strings.Contains(body, `"candidatesTokenCount":2`) {
		t.Errorf("usageMetadata frame missing: %s", body)
	}
	if n := strings.Count(body, "data: "); n < 4 {
		t.Errorf("frame count = %d, want >= 4 (2 deltas + finish + usage)", n)
	}
}

// TestGeminiStreamMidStreamBreak 验收 M3.4：首帧落地后上游断流，
// Gemini 入站侧仍补 finishReason=STOP 收尾帧，客户端拿到优雅结束
// 而非悬挂连接。
func TestGeminiStreamMidStreamBreak(t *testing.T) {
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

	resp := postGemini(t, gw.URL+"/v1beta/models/gemini-x:streamGenerateContent",
		geminiBody, testGatewayToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, b)
	}
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	if !strings.Contains(body, `"text":"par"`) {
		t.Errorf("shipped delta frame missing: %s", body)
	}
	if !strings.Contains(body, `"finishReason":"STOP"`) { // 未见 finish 的断流补 STOP
		t.Errorf("graceful finishReason frame missing: %s", body)
	}
}
