package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/translate"
)

func baseSpec() Spec {
	return Spec{ProviderName: "mock", BaseURL: "https://api.mock.test", APIKey: "goog-test"}
}

func sampleRequest(stream bool) *schema.UnifiedRequest {
	return &schema.UnifiedRequest{
		Model:  "gemini-x",
		Stream: stream,
		Messages: []schema.Message{
			{Role: schema.RoleSystem, Content: schema.NewTextContent("be brief")},
			{Role: schema.RoleUser, Content: schema.NewTextContent("hi")},
			{Role: schema.RoleAssistant, Content: schema.NewTextContent("hello")},
			{Role: schema.RoleUser, Content: schema.NewTextContent("and?")},
		},
	}
}

func httpResponse(t *testing.T, status int, body string) *http.Response {
	t.Helper()
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
}

func TestTranslateWireNonStreaming(t *testing.T) {
	a, err := New(baseSpec())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	call, err := a.Translate(context.Background(), sampleRequest(false))
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	want := "https://api.mock.test/v1beta/models/gemini-x:generateContent"
	if call.URL != want {
		t.Errorf("URL = %q, want %q", call.URL, want)
	}
	if got := call.Header.Get("x-goog-api-key"); got != "goog-test" {
		t.Errorf("x-goog-api-key = %q", got)
	}
	assertGeminiBody(t, call.Body)
	if call.Stream {
		t.Error("Stream should be false")
	}
}

func TestTranslateWireStreaming(t *testing.T) {
	a, err := New(baseSpec())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	call, err := a.Translate(context.Background(), sampleRequest(true))
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	want := "https://api.mock.test/v1beta/models/gemini-x:streamGenerateContent?alt=sse"
	if call.URL != want {
		t.Errorf("URL = %q, want %q", call.URL, want)
	}
	if !call.Stream {
		t.Error("Stream should be true")
	}
}

// assertGeminiBody 校验 wire 语义：system 抽到 systemInstruction、
// assistant 映射 model、model 名不进请求体（在 URL 路径里）。
func assertGeminiBody(t *testing.T, body []byte) {
	t.Helper()
	var wire translate.GeminiRequest
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("body not Gemini JSON: %v", err)
	}
	if wire.SystemInstruction == nil ||
		len(wire.SystemInstruction.Parts) != 1 ||
		wire.SystemInstruction.Parts[0].Text != "be brief" {
		t.Errorf("systemInstruction = %+v", wire.SystemInstruction)
	}
	if len(wire.Contents) != 3 {
		t.Fatalf("contents = %d, want 3 (system hoisted)", len(wire.Contents))
	}
	if wire.Contents[0].Role != "user" || wire.Contents[1].Role != "model" {
		t.Errorf("roles = %q/%q, want user/model", wire.Contents[0].Role, wire.Contents[1].Role)
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(body, &raw)
	if _, ok := raw["model"]; ok {
		t.Error("model must not appear in the body (it lives in the URL path)")
	}
	if _, ok := raw["tools"]; ok {
		t.Error("tools must not be sent for tool-less requests")
	}
}

func TestParseAggregated(t *testing.T) {
	a, err := New(baseSpec())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body := `{"responseId":"resp-1","candidates":[{"content":{"role":"model",` +
		`"parts":[{"text":"hello"}]},"finishReason":"STOP","index":0}],` +
		`"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":6,"totalTokenCount":10},` +
		`"modelVersion":"gemini-x"}`
	call := &provider.ProviderCall{Model: "gemini-x"}
	resp, err := a.Parse(context.Background(), call, httpResponse(t, 200, body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if resp.ID != "resp-1" {
		t.Errorf("ID = %q", resp.ID)
	}
	if resp.ProviderName != "mock" {
		t.Errorf("ProviderName = %q", resp.ProviderName)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content.TextOf() != "hello" {
		t.Errorf("choices = %+v", resp.Choices)
	}
	if resp.Choices[0].FinishReason != schema.FinishStop {
		t.Errorf("finish = %q, want stop", resp.Choices[0].FinishReason)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 4 || resp.Usage.CompletionTokens != 6 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestParseUpstreamError(t *testing.T) {
	a, err := New(baseSpec())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = a.Parse(context.Background(), nil, httpResponse(t, 400,
		`{"error":{"code":400,"message":"bad","status":"INVALID_ARGUMENT"}}`))
	ue, ok := provider.IsUpstream(err)
	if !ok {
		t.Fatalf("err = %v, want UpstreamError", err)
	}
	if ue.Status != 400 {
		t.Errorf("Status = %d, want 400", ue.Status)
	}
}
