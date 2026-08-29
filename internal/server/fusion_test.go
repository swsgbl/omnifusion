package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/intelligence"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/store"
)

// fusionFixture 起一个装配 Fusion 的被测网关：上游按请求模型返回可
// 区分文本与固定用量（4/2/6），计数逐模型；两 provider 各一个模型。
type fusionFixture struct {
	gw        *httptest.Server
	srv       *Server
	hits      map[string]int
	status    int    // 非 0 → 上游统一返回该状态码（造全失败）
	failModel string // 非空 → 该模型的请求返回 500（造单成员失败）
	mu        sync.Mutex
}

func newFusionFixture(t *testing.T, runner *intelligence.FusionRunner) *fusionFixture {
	t.Helper()
	fx := &fusionFixture{hits: map[string]int{}}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		model := `"model":"`
		m := ""
		if i := strings.Index(string(body), model); i >= 0 {
			rest := string(body)[i+len(model):]
			if j := strings.Index(rest, `"`); j >= 0 {
				m = rest[:j]
			}
		}
		fx.mu.Lock()
		fx.hits[m]++
		st, fail := fx.status, m == fx.failModel
		fx.mu.Unlock()
		if st != 0 || fail {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"message":"boom"}}`)
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
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), st))
	s.SetRouter(&routing.Router{Providers: []provider.Provider{a, b}})
	s.SetCache(intelligence.NewSemCache(st, time.Hour, 64))
	if runner != nil {
		s.SetFusion(runner)
	}
	fx.srv = s
	fx.gw = httptest.NewServer(s.Handler())
	t.Cleanup(fx.gw.Close)
	return fx
}

// twoMemberRunner：a/b 扇出（quorum=2），judge 显式走 a。
func twoMemberRunner() *intelligence.FusionRunner {
	return &intelligence.FusionRunner{
		Members: []intelligence.FusionMember{
			{Provider: "a", Model: "model-a"},
			{Provider: "b", Model: "model-b"},
		},
		Judge:  intelligence.FusionMember{Provider: "a", Model: "model-a"},
		Quorum: 2,
	}
}

func fusionBody() string {
	return `{"model":"@fusion","messages":[{"role":"user","content":"hi"}]}`
}

// TestFusionChatEndToEnd @fusion 非流式端到端：扇出两成员 + judge 合成，
// 终稿文本来自 judge 模型，用量为三次调用总和，响应头标记 synthesized。
func TestFusionChatEndToEnd(t *testing.T) {
	fx := newFusionFixture(t, twoMemberRunner())
	resp := postAuthed(t, fx.gw.URL+"/v1/chat/completions", fusionBody())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, b)
	}
	if got := resp.Header.Get("X-OmniFusion-Fusion"); got != "synthesized" {
		t.Errorf("fusion header = %q, want synthesized", got)
	}
	var out schema.Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if text := out.Choices[0].Message.Content.Parts[0].Text; text != "ok-model-a" {
		t.Errorf("content = %q, want ok-model-a（judge 模型产出）", text)
	}
	if out.Usage == nil || out.Usage.PromptTokens != 12 || out.Usage.TotalTokens != 18 {
		t.Errorf("usage = %+v, want 3 次调用求和 12/6/18", out.Usage)
	}
	fx.mu.Lock()
	defer fx.mu.Unlock()
	if fx.hits["model-a"] != 2 || fx.hits["model-b"] != 1 {
		t.Errorf("hits = %v, want model-a=2（扇出+judge）model-b=1", fx.hits)
	}
}

// TestFusionAnthropicEndpoint @fusion 经 /v1/messages（Anthropic 形）
// 端到端可用：翻译层把 IR 终稿转回 Anthropic 响应。
func TestFusionAnthropicEndpoint(t *testing.T) {
	fx := newFusionFixture(t, twoMemberRunner())
	body := `{"model":"@fusion","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
	resp := postAuthed(t, fx.gw.URL+"/v1/messages", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, b)
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Content) == 0 || out.Content[0].Type != "text" || out.Content[0].Text != "ok-model-a" {
		t.Errorf("content = %+v, want judge 终稿 text 块", out.Content)
	}
}

// TestFusionStreamRejected @fusion + stream=true → 400 明确报错。
func TestFusionStreamRejected(t *testing.T) {
	fx := newFusionFixture(t, twoMemberRunner())
	resp := postAuthed(t, fx.gw.URL+"/v1/chat/completions",
		`{"model":"@fusion","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400（v1 不支持流式合成）", resp.StatusCode)
	}
}

// TestFusionNotConfigured 未装配 fusion → 400（不静默降级为普通分发）。
func TestFusionNotConfigured(t *testing.T) {
	fx := newFusionFixture(t, nil)
	resp := postAuthed(t, fx.gw.URL+"/v1/chat/completions", fusionBody())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestFusionAllMembersFail502 全成员失败 → 502 upstream_error。
func TestFusionAllMembersFail502(t *testing.T) {
	fx := newFusionFixture(t, twoMemberRunner())
	fx.mu.Lock()
	fx.status = http.StatusInternalServerError
	fx.mu.Unlock()
	resp := postAuthed(t, fx.gw.URL+"/v1/chat/completions", fusionBody())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "fusion failed") {
		t.Errorf("body = %s, want 含 fusion failed", b)
	}
}

// TestFusionSingleDegraded 仅 1 成员成功（< quorum=2）→ 降级直通该
// 成员 + X-OmniFusion-Fusion: single。
func TestFusionSingleDegraded(t *testing.T) {
	fx := newFusionFixture(t, twoMemberRunner())
	fx.mu.Lock()
	fx.failModel = "model-b" // b 扇出失败；仅 a 成功 → 直通 a
	fx.mu.Unlock()
	resp := postAuthed(t, fx.gw.URL+"/v1/chat/completions", fusionBody())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200（降级直通）", resp.StatusCode)
	}
	if got := resp.Header.Get("X-OmniFusion-Fusion"); got != "single" {
		t.Errorf("fusion header = %q, want single", got)
	}
	var out schema.Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if text := out.Choices[0].Message.Content.Parts[0].Text; text != "ok-model-a" {
		t.Errorf("content = %q, want ok-model-a（直通唯一成功成员）", text)
	}
}
