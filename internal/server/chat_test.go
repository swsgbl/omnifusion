package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
)

func TestChatCompletionsNonStreaming(t *testing.T) {
	var gotAuth, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"id": "chatcmpl-abc",
			"object": "chat.completion",
			"created": 1700000001,
			"model": "mock-model",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "pong"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 4, "completion_tokens": 2, "total_tokens": 6}
		}`)
	}))
	defer upstream.Close()

	adapter, err := openai_compat.New(openai_compat.Spec{
		ProviderName: "mock",
		BaseURL:      upstream.URL + "/v1",
		APIKey:       "sk-test",
	})
	if err != nil {
		t.Fatalf("openai_compat.New: %v", err)
	}
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	s.SetRouter(&routing.Router{Providers: []provider.Provider{adapter}})

	gw := httptest.NewServer(s.Handler())
	defer gw.Close()

	resp := postAuthed(t, gw.URL+"/v1/chat/completions",
		`{"model":"mock-model","messages":[{"role":"user","content":"ping"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	var parsed schema.Response
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if parsed.ID != "chatcmpl-abc" || parsed.Model != "mock-model" {
		t.Errorf("unexpected response: %+v", parsed)
	}
	if len(parsed.Choices) != 1 || parsed.Choices[0].Message.Content.TextOf() != "pong" {
		t.Errorf("unexpected choices: %+v", parsed.Choices)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("upstream Authorization = %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"model":"mock-model"`) {
		t.Errorf("upstream body = %s", gotBody)
	}
}

func TestChatCompletionsValidation(t *testing.T) {
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	adapter, err := openai_compat.New(openai_compat.Spec{
		ProviderName: "mock",
		BaseURL:      "http://127.0.0.1:1/v1",
		APIKey:       "k",
	})
	if err != nil {
		t.Fatalf("openai_compat.New: %v", err)
	}
	s.SetRouter(&routing.Router{Providers: []provider.Provider{adapter}})
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()

	cases := []struct {
		name string
		body string
		want int
	}{
		{"invalid json", `{`, http.StatusBadRequest},
		{"missing model", `{"messages":[{"role":"user","content":"x"}]}`, http.StatusBadRequest},
		{"missing messages", `{"model":"m"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postAuthed(t, gw.URL+"/v1/chat/completions", tc.body)
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("status = %d, want %d, body = %s", resp.StatusCode, tc.want, body)
			}
		})
	}
}

func TestChatCompletionsNoProviders(t *testing.T) {
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()

	resp := postAuthed(t, gw.URL+"/v1/chat/completions",
		`{"model":"m","messages":[{"role":"user","content":"x"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

func TestChatCompletionsUpstreamFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"boom"}`)
	}))
	defer upstream.Close()

	adapter, err := openai_compat.New(openai_compat.Spec{
		ProviderName: "mock",
		BaseURL:      upstream.URL + "/v1",
		APIKey:       "k",
	})
	if err != nil {
		t.Fatalf("openai_compat.New: %v", err)
	}
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	s.SetRouter(&routing.Router{Providers: []provider.Provider{adapter}})
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()

	resp := postAuthed(t, gw.URL+"/v1/chat/completions",
		`{"model":"m","messages":[{"role":"user","content":"x"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 502, body = %s", resp.StatusCode, body)
	}
}
