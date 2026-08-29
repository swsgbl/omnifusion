package routing

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
)

func newMockAdapter(t *testing.T, name, upstreamURL string) provider.Provider {
	t.Helper()
	a, err := openai_compat.New(openai_compat.Spec{
		ProviderName: name,
		BaseURL:      upstreamURL + "/v1",
		APIKey:       "k",
	})
	if err != nil {
		t.Fatalf("New(%s): %v", name, err)
	}
	return a
}

func testRequest() *schema.UnifiedRequest {
	return &schema.UnifiedRequest{
		Model:    "m",
		Messages: []schema.Message{{Role: "user", Content: schema.NewTextContent("hi")}},
	}
}

func okCompletion(model string) string {
	return `{
		"id": "chatcmpl-x",
		"object": "chat.completion",
		"created": 1,
		"model": "` + model + `",
		"choices": [{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
		"usage": {"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
	}`
}

func TestDispatchFirstHealthyWins(t *testing.T) {
	hits := map[string]int{}
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits["a"]++
		io.WriteString(w, okCompletion("model-a"))
	}))
	defer upA.Close()
	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits["b"]++
		io.WriteString(w, okCompletion("model-b"))
	}))
	defer upB.Close()

	r := &Router{Providers: []provider.Provider{
		newMockAdapter(t, "a", upA.URL),
		newMockAdapter(t, "b", upB.URL),
	}}
	resp, attempts, err := r.Dispatch(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.ProviderName != "a" {
		t.Errorf("ProviderName = %q, want a", resp.ProviderName)
	}
	if len(attempts) != 1 {
		t.Errorf("attempts = %d, want 1 (second provider must not be called)", len(attempts))
	}
	if hits["b"] != 0 {
		t.Errorf("provider b hit %d times", hits["b"])
	}
}

func TestDispatchFallsBackOnUpstreamError(t *testing.T) {
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":"rate limited"}`)
	}))
	defer upA.Close()
	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okCompletion("model-b"))
	}))
	defer upB.Close()

	r := &Router{Providers: []provider.Provider{
		newMockAdapter(t, "flaky", upA.URL),
		newMockAdapter(t, "steady", upB.URL),
	}}
	resp, attempts, err := r.Dispatch(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.ProviderName != "steady" {
		t.Errorf("ProviderName = %q, want steady", resp.ProviderName)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(attempts))
	}
	if attempts[0].Err == nil {
		t.Error("first attempt should record an error")
	}
	if ue, ok := provider.IsUpstream(attempts[0].Err); !ok || ue.Status != http.StatusTooManyRequests {
		t.Errorf("first attempt error = %v", attempts[0].Err)
	}
}

func TestDispatchAllFail(t *testing.T) {
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"boom"}`)
	}))
	defer upA.Close()

	r := &Router{Providers: []provider.Provider{newMockAdapter(t, "a", upA.URL)}}
	_, attempts, err := r.Dispatch(context.Background(), testRequest())
	if err == nil {
		t.Fatal("expected error")
	}
	var de *DispatchError
	if !errors.As(err, &de) {
		t.Fatalf("err type = %T", err)
	}
	if len(attempts) != 1 {
		t.Errorf("attempts = %d, want 1", len(attempts))
	}
}

func TestDispatchTransportFailureFallsThrough(t *testing.T) {
	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okCompletion("model-b"))
	}))
	defer upB.Close()

	// 127.0.0.1:1 is closed: transport error must not abort the chain.
	bad := newMockAdapter(t, "bad", "http://127.0.0.1:1")
	r := &Router{Providers: []provider.Provider{bad, newMockAdapter(t, "good", upB.URL)}}
	resp, attempts, err := r.Dispatch(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.ProviderName != "good" || len(attempts) != 2 {
		t.Errorf("resp = %q attempts = %d", resp.ProviderName, len(attempts))
	}
}

func TestDispatchNoProviders(t *testing.T) {
	r := &Router{}
	_, _, err := r.Dispatch(context.Background(), testRequest())
	if err == nil {
		t.Fatal("expected error for empty provider list")
	}
}
