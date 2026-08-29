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

// newAuthGateway 搭一个带 mock 上游、已装配网关 key 的完整网关。
func newAuthGateway(t *testing.T) *httptest.Server {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"id": "chatcmpl-ok", "object": "chat.completion", "created": 1, "model": "m",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "pong"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
		}`)
	}))
	t.Cleanup(upstream.Close)

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
	t.Cleanup(gw.Close)
	return gw
}

func TestGatewayKeyMissingOrWrong(t *testing.T) {
	gw := newAuthGateway(t)
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`

	cases := []struct {
		name string
		send func() *http.Response
	}{
		{"missing header", func() *http.Response { return postBare(t, gw.URL+"/v1/chat/completions", body) }},
		{"wrong key", func() *http.Response {
			req, err := http.NewRequest(http.MethodPost, gw.URL+"/v1/chat/completions", strings.NewReader(body))
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer ofg-wrong")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			return resp
		}},
		{"no bearer prefix", func() *http.Response {
			req, err := http.NewRequest(http.MethodPost, gw.URL+"/v1/chat/completions", strings.NewReader(body))
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", testGatewayToken)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			return resp
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.send()
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				b, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want 401, body = %s", resp.StatusCode, b)
			}
			var env struct {
				Error struct {
					Type string `json:"type"`
				} `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if env.Error.Type != "authentication_error" {
				t.Errorf("error type = %q, want authentication_error", env.Error.Type)
			}
		})
	}
}

func TestGatewayKeyAccepted(t *testing.T) {
	gw := newAuthGateway(t)
	resp := postAuthed(t, gw.URL+"/v1/chat/completions",
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200, body = %s", resp.StatusCode, b)
	}
}

func TestHealthzStaysOpen(t *testing.T) {
	gw := newAuthGateway(t)
	resp, err := http.Get(gw.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz status = %d, want 200 without auth", resp.StatusCode)
	}
}

// TestGatewayKeyFailClosed 验证 token 未装配时数据面一律拒绝（绝不裸奔）。
func TestGatewayKeyFailClosed(t *testing.T) {
	s := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil) // 未 SetGatewayToken
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()

	resp := postBare(t, gw.URL+"/v1/chat/completions",
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 fail-closed", resp.StatusCode)
	}
}
