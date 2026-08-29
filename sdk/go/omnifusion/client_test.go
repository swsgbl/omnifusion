package omnifusion

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGateway 是贴网关行为的假服务：按 path 分派。
type fakeGateway struct {
	t       *testing.T
	key     string
	srv     *httptest.Server
	gotAuth string
	gotBody map[string]any
}

func newFakeGateway(t *testing.T) *fakeGateway {
	t.Helper()
	g := &fakeGateway{t: t, key: "ofg-test"}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.gotAuth = r.Header.Get("Authorization")
		if r.URL.Path == "/v1/models" {
			if g.gotAuth != "Bearer "+g.key {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"data": []ModelInfo{{ID: "a:1"}, {ID: "b:2"}}})
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		g.gotBody = req
		if g.gotAuth != "Bearer "+g.key {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"bad key"}}`))
			return
		}
		if stream, _ := req["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, piece := range []string{"Hello", ", ", "wor", "ld"} {
				w.Write([]byte(`data: {"id":"1","choices":[{"index":0,"delta":{"content":"` + piece + `"}}]}` + "\n\n"))
			}
			w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"mock-1","model":"a:1","choices":[{"index":0,` +
			`"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],` +
			`"usage":{"prompt_tokens":7,"completion_tokens":4,"total_tokens":11}}`))
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func TestChatNonStream(t *testing.T) {
	g := newFakeGateway(t)
	c := NewClient(g.srv.URL, g.key)
	resp, err := c.Chat(context.Background(), &ChatRequest{
		Model:    "a:1",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Text() != "ok" || resp.Usage.TotalTokens != 11 {
		t.Fatalf("resp = %+v", resp)
	}
	if got := g.gotBody["stream"]; got != nil {
		t.Errorf("request carried stream=%v, want omitted", got)
	}
	if g.gotBody["model"] != "a:1" {
		t.Errorf("model = %v", g.gotBody["model"])
	}
	// CacheHit 头存在时可见。
	if resp.CacheHit() {
		t.Error("CacheHit = true without header")
	}
}

func TestChatUnauthorized(t *testing.T) {
	g := newFakeGateway(t)
	c := NewClient(g.srv.URL, "ofg-wrong")
	_, err := c.Chat(context.Background(), &ChatRequest{Model: "a:1", Messages: []Message{{Role: "user", Content: "x"}}})
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != 401 {
		t.Fatalf("err = %v, want 401 StatusError", err)
	}
	if !strings.Contains(se.Error(), "bad key") {
		t.Errorf("StatusError body missing detail: %v", se)
	}
}

func TestChatStream(t *testing.T) {
	g := newFakeGateway(t)
	c := NewClient(g.srv.URL, g.key)
	s, err := c.ChatStream(context.Background(), &ChatRequest{
		Model:    "a:1",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	defer s.Close()
	n := 0
	for s.Next() {
		n++
		if s.Chunk() == nil {
			t.Fatal("Next true but Chunk nil")
		}
	}
	if err := s.Err(); err != nil {
		t.Fatalf("stream err: %v", err)
	}
	if n != 4 {
		t.Fatalf("chunks = %d, want 4", n)
	}
	if got := s.Text(); got != "Hello, world" {
		t.Fatalf("accumulated text = %q, want %q", got, "Hello, world")
	}
	if got := g.gotBody["stream"]; got != true {
		t.Errorf("request stream = %v, want true", got)
	}
	// 关流后 Next 恒 false。
	if s.Next() {
		t.Error("Next after done = true")
	}
}

func TestStreamUnauthorized(t *testing.T) {
	g := newFakeGateway(t)
	c := NewClient(g.srv.URL, "ofg-wrong")
	s, err := c.ChatStream(context.Background(), &ChatRequest{Model: "a:1"})
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != 401 {
		t.Fatalf("err = %v, want 401 StatusError", err)
	}
	if s != nil {
		t.Fatal("Stream non-nil on error")
	}
}

func TestModels(t *testing.T) {
	g := newFakeGateway(t)
	c := NewClient(g.srv.URL, g.key)
	models, err := c.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 2 || models[0].ID != "a:1" {
		t.Fatalf("models = %+v", models)
	}
}

func TestBaseURLTrailingSlash(t *testing.T) {
	g := newFakeGateway(t)
	c := NewClient(g.srv.URL+"/", g.key)
	if _, err := c.Models(context.Background()); err != nil {
		t.Fatalf("trailing slash base: %v", err)
	}
}
