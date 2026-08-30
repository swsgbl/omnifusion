// smart_test.go 覆盖 @smart 端到端：易/难请求分别落弱/强档
// （echo 上游按模型回可辨文本）、弱档失败 failover 强档、流式路径、
// 边界 400（@smart:x / 未装配 / header smart）。
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/intelligence"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
)

// smartFixture：上游按请求模型回 ok-<model>（可辨弱/强落点），流式
// 请求回 SSE；failModel 非 0 时该模型统一 500（造弱档失败）。
type smartFixture struct {
	gw        *httptest.Server
	hits      map[string]int
	failModel string
	mu        sync.Mutex
}

func newSmartFixture(t *testing.T, attachSmart bool) *smartFixture {
	t.Helper()
	fx := &smartFixture{hits: map[string]int{}}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		m := ""
		if i := strings.Index(s, `"model":"`); i >= 0 {
			rest := s[i+len(`"model":"`):]
			if j := strings.Index(rest, `"`); j >= 0 {
				m = rest[:j]
			}
		}
		fx.mu.Lock()
		fx.hits[m]++
		fail := m == fx.failModel
		fx.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"message":"boom"}}`)
			return
		}
		if strings.Contains(s, `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"%s\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok-%s\"},\"finish_reason\":null}]}\n\n", m, m)
			io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"c-%s","object":"chat.completion","created":1,"model":"%s",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok-%s"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`, m, m, m)
	}))
	t.Cleanup(up.Close)

	a, err := openai_compat.New(openai_compat.Spec{ProviderName: "a", BaseURL: up.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("adapter a: %v", err)
	}
	b, err := openai_compat.New(openai_compat.Spec{ProviderName: "b", BaseURL: up.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("adapter b: %v", err)
	}
	router := &routing.Router{Providers: []provider.Provider{a, b}}
	if attachSmart {
		ml := intelligence.NewMLRouter(
			intelligence.MLTarget{Provider: "a", Model: "model-a"}, // 弱档
			intelligence.MLTarget{Provider: "b", Model: "model-b"}, // 强档
		)
		router.Smart = func(req *schema.UnifiedRequest) routing.SmartPlan {
			d := ml.Route(req)
			members := make([]routing.ComboMember, 0, len(d.Members))
			for _, m := range d.Members {
				members = append(members, routing.ComboMember{Provider: m.Provider, Model: m.Model})
			}
			return routing.SmartPlan{Members: members, Tier: d.Tier, Difficulty: d.Difficulty}
		}
	}
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	s.SetRouter(router)
	fx.gw = httptest.NewServer(s.Handler())
	t.Cleanup(fx.gw.Close)
	return fx
}

const (
	smartEasyBody = `{"model":"@smart","messages":[{"role":"user","content":"hi there!"}]}`
	smartHardBody = `{"model":"@smart","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"analyze and debug:\nfunc main() { panic(1) }"},` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBOR"}}]}],` +
		`"tools":[{"type":"function","function":{"name":"run","description":"run code"}}]}`
)

func TestSmartEasyToWeakStrongToStrong(t *testing.T) {
	fx := newSmartFixture(t, true)
	for _, tc := range []struct {
		name, body, wantModel string
	}{
		{"易请求落弱档", smartEasyBody, "model-a"},
		{"难请求落强档", smartHardBody, "model-b"},
	} {
		resp := postAuthed(t, fx.gw.URL+"/v1/chat/completions", tc.body)
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("%s: status = %d, body = %s", tc.name, resp.StatusCode, b)
		}
		var out schema.Response
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("%s: decode: %v", tc.name, err)
		}
		if got := out.Choices[0].Message.Content.TextOf(); got != "ok-"+tc.wantModel {
			t.Errorf("%s: content = %q, want ok-%s", tc.name, got, tc.wantModel)
		}
	}
	fx.mu.Lock()
	defer fx.mu.Unlock()
	if fx.hits["model-a"] == 0 || fx.hits["model-b"] == 0 {
		t.Errorf("上游命中 = %v, want 弱/强各 ≥1 次", fx.hits)
	}
}

func TestSmartFailoverWeakToStrong(t *testing.T) {
	fx := newSmartFixture(t, true)
	fx.mu.Lock()
	fx.failModel = "model-a" // 弱档 500
	fx.mu.Unlock()
	resp := postAuthed(t, fx.gw.URL+"/v1/chat/completions", smartEasyBody)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, b)
	}
	var out schema.Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := out.Choices[0].Message.Content.TextOf(); got != "ok-model-b" {
		t.Errorf("content = %q, want ok-model-b（弱档失败 failover 强档）", got)
	}
}

func TestSmartStreaming(t *testing.T) {
	fx := newSmartFixture(t, true)
	resp := postAuthed(t, fx.gw.URL+"/v1/chat/completions",
		`{"model":"@smart","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	frames := readSSE(t, resp.Body)
	if len(frames) == 0 || !strings.Contains(frames[0], "ok-model-a") {
		t.Errorf("首帧 = %v, want 弱档产出的 ok-model-a", frames)
	}
}

func TestSmartDirectiveValidation(t *testing.T) {
	fx := newSmartFixture(t, true) // 已装配
	resp := postAuthed(t, fx.gw.URL+"/v1/chat/completions",
		`{"model":"@smart:m","messages":[{"role":"user","content":"hi"}]}`)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("@smart:m: status = %d, want 400", resp.StatusCode)
	}
	// header 形式拒绝（smart 仅 model 内嵌）
	req, _ := http.NewRequest(http.MethodPost, fx.gw.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testGatewayToken)
	req.Header.Set(routing.HeaderStrategy, "smart")
	hresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	io.Copy(io.Discard, hresp.Body)
	hresp.Body.Close()
	if hresp.StatusCode != http.StatusBadRequest {
		t.Errorf("header smart: status = %d, want 400", hresp.StatusCode)
	}

	// 未装配：@smart 直接 400
	plain := newSmartFixture(t, false)
	resp2 := postAuthed(t, plain.gw.URL+"/v1/chat/completions", smartEasyBody)
	io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("未装配 @smart: status = %d, want 400", resp2.StatusCode)
	}
}
