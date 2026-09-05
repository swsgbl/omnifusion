// parse_test.go 覆盖上游响应解析：成功面与错误面。
package openai_compat

import (
	"context"
	"github.com/swsgbl/omnifusion/internal/provider"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseSuccess(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"id": "chatcmpl-1",
			"object": "chat.completion",
			"created": 1700000000,
			"model": "real-model",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "hello"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}
		}`)
	}))
	defer upstream.Close()

	a, err := New(Spec{ProviderName: "mock", BaseURL: upstream.URL + "/v1", APIKey: "sk-test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	call, err := a.Translate(context.Background(), sampleRequest("real-model"))
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}

	httpReq, err := http.NewRequest(call.Method, call.URL, strings.NewReader(string(call.Body)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	httpReq.Header = call.Header
	resp, err := a.HTTPClient().Do(httpReq)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	parsed, err := a.Parse(context.Background(), call, resp)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if parsed.ProviderName != "mock" {
		t.Errorf("ProviderName = %q", parsed.ProviderName)
	}
	if parsed.ID != "chatcmpl-1" || parsed.Model != "real-model" {
		t.Errorf("unexpected id/model: %q %q", parsed.ID, parsed.Model)
	}
	if len(parsed.Choices) != 1 || parsed.Choices[0].Message.Content.TextOf() != "hello" {
		t.Errorf("unexpected choices: %+v", parsed.Choices)
	}
	if parsed.Usage == nil || parsed.Usage.TotalTokens != 8 {
		t.Errorf("unexpected usage: %+v", parsed.Usage)
	}
}

func TestParseUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error": {"message": "rate limited"}}`)
	}))
	defer upstream.Close()

	a, err := New(Spec{ProviderName: "mock", BaseURL: upstream.URL + "/v1", APIKey: "sk-test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	call, err := a.Translate(context.Background(), sampleRequest("m"))
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	httpReq, err := http.NewRequest(call.Method, call.URL, strings.NewReader(string(call.Body)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	httpReq.Header = call.Header
	resp, err := a.HTTPClient().Do(httpReq)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, err = a.Parse(context.Background(), call, resp)
	ue, ok := provider.IsUpstream(err)
	if !ok {
		t.Fatalf("expected UpstreamError, got %v", err)
	}
	if ue.Status != http.StatusTooManyRequests {
		t.Errorf("Status = %d", ue.Status)
	}
	if !strings.Contains(string(ue.Body), "rate limited") {
		t.Errorf("Body = %q", ue.Body)
	}
}

// TestParseEmptyChoicesIsUpstreamError 200+零候选是上游伪成功（实测
// 某商偶发），必须报 UpstreamError 让路由 failover——绝不能当成功
// 回给客户端，更不能进语义缓存。
func TestParseEmptyChoicesIsUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"","object":"","created":0,"model":"m","choices":null}`)
	}))
	defer upstream.Close()

	a, err := New(Spec{ProviderName: "mock", BaseURL: upstream.URL + "/v1", APIKey: "sk-test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	call, err := a.Translate(context.Background(), sampleRequest("m"))
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	httpReq, err := http.NewRequest(call.Method, call.URL, strings.NewReader(string(call.Body)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	httpReq.Header = call.Header
	resp, err := a.HTTPClient().Do(httpReq)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, err = a.Parse(context.Background(), call, resp)
	ue, ok := provider.IsUpstream(err)
	if !ok {
		t.Fatalf("expected UpstreamError for empty choices, got %v", err)
	}
	if ue.Status != http.StatusOK {
		t.Errorf("Status = %d, want 200", ue.Status)
	}
}
