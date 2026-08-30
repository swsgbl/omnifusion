package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
)

func baseSpec() Spec {
	return Spec{ProviderName: "mock", BaseURL: "https://api.mock.test", APIKey: "sk-ant-test"}
}

func sampleRequest() *schema.UnifiedRequest {
	return &schema.UnifiedRequest{
		Model: "claude-x",
		Messages: []schema.Message{
			{Role: schema.RoleSystem, Content: schema.NewTextContent("be brief")},
			{Role: schema.RoleUser, Content: schema.NewTextContent("hi")},
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

func TestTranslateWire(t *testing.T) {
	a, err := New(baseSpec())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	call, err := a.Translate(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if want := "https://api.mock.test/v1/messages"; call.URL != want {
		t.Errorf("URL = %q, want %q", call.URL, want)
	}
	if got := call.Header.Get("x-api-key"); got != "sk-ant-test" {
		t.Errorf("x-api-key = %q", got)
	}
	if got := call.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q", got)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(call.Body, &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if _, ok := body["system"]; !ok {
		t.Error("system must be hoisted to the top level")
	}
	if got := strings.Trim(string(body["max_tokens"]), `.`); got == "" {
		t.Error("max_tokens must be present (required upstream)")
	}
	if _, ok := body["tools"]; ok {
		t.Error("tools must not be sent for tool-less requests")
	}
	if call.Stream {
		t.Error("Stream should be false")
	}
}

func TestTranslateDefaultsBaseURL(t *testing.T) {
	a, err := New(Spec{ProviderName: "m", APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	call, err := a.Translate(context.Background(), &schema.UnifiedRequest{
		Model:    "claude-x",
		Messages: []schema.Message{{Role: "user", Content: schema.NewTextContent("hi")}},
	})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if want := DefaultBaseURL + Path; call.URL != want {
		t.Errorf("URL = %q, want %q", call.URL, want)
	}
}

func TestParseAggregated(t *testing.T) {
	a, err := New(baseSpec())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body := `{"id":"msg_01ABC","type":"message","role":"assistant","model":"claude-x",` +
		`"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":3,"output_tokens":5}}`
	call := &provider.ProviderCall{Model: "claude-x"}
	resp, err := a.Parse(context.Background(), call, httpResponse(t, 200, body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if resp.ID != "01ABC" {
		t.Errorf("ID = %q, want msg_ prefix stripped", resp.ID)
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
	if resp.Usage == nil || resp.Usage.PromptTokens != 3 || resp.Usage.CompletionTokens != 5 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestParseUpstreamError(t *testing.T) {
	a, err := New(baseSpec())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = a.Parse(context.Background(), nil,
		httpResponse(t, 429, `{"type":"error","error":{"type":"rate_limit_error"}}`))
	ue, ok := provider.IsUpstream(err)
	if !ok {
		t.Fatalf("err = %v, want UpstreamError", err)
	}
	if ue.Status != 429 {
		t.Errorf("Status = %d, want 429", ue.Status)
	}
	if ue.Provider != "mock" {
		t.Errorf("Provider = %q", ue.Provider)
	}
}
