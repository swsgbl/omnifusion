package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/a2a"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
)

// newA2AGateway 装配带 A2A 端点的测试网关：上游 provider 指向
// upstream URL（OpenAI 兼容）。
func newA2AGateway(t *testing.T, upstream *httptest.Server, defaultModel string) *httptest.Server {
	t.Helper()
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
	s.SetA2A(a2a.BuildCard(a2a.CardOptions{
		BaseURL: "http://gw.test", Version: "test", DefaultModel: defaultModel, Streaming: true,
	}), defaultModel)
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	return gw
}

func TestA2ACardPublic(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	gw := newA2AGateway(t, upstream, "m")

	resp, err := http.Get(gw.URL + "/.well-known/agent-card.json") // 公开：无鉴权头
	if err != nil {
		t.Fatalf("GET card: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("card status = %d", resp.StatusCode)
	}
	var card map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatalf("card json: %v", err)
	}
	iface := card["supportedInterfaces"].([]any)[0].(map[string]any)
	if iface["protocolBinding"] != "JSONRPC" || iface["protocolVersion"] != "1.0" {
		t.Fatalf("card interface = %+v", iface)
	}
}

func TestA2ARPCRequiresGatewayKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	gw := newA2AGateway(t, upstream, "m")

	resp := postBare(t, gw.URL+"/rpc", `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bare /rpc status = %d, want 401", resp.StatusCode)
	}
}

func TestA2ASendMessageMessageOnly(t *testing.T) {
	var upstreamSawModel, upstreamSawContent string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(b, &req)
		upstreamSawModel = req.Model
		if len(req.Messages) > 0 {
			upstreamSawContent = req.Messages[0].Content
		}
		io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"mock-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
	}))
	defer upstream.Close()
	gw := newA2AGateway(t, upstream, "mock-model")

	body := `{"jsonrpc":"2.0","id":"req-1","method":"SendMessage","params":{` +
		`"message":{"role":"ROLE_USER","messageId":"m1","contextId":"ctx-9",` +
		`"parts":[{"text":"ping"}],"metadata":{"model":"mock-model"}}}}`
	resp := postAuthed(t, gw.URL+"/rpc", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, b)
	}
	var rpc struct {
		ID     string `json:"id"`
		Result *struct {
			Message *struct {
				Role  string `json:"role"`
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
				Metadata json.RawMessage `json:"metadata"`
			} `json:"message"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rpc.ID != "req-1" || rpc.Result == nil || rpc.Result.Message == nil {
		t.Fatalf("rpc = %+v", rpc)
	}
	m := rpc.Result.Message
	if m.Role != "ROLE_AGENT" || len(m.Parts) != 1 || m.Parts[0].Text != "pong" {
		t.Fatalf("message = %+v", m)
	}
	var meta struct {
		Usage struct {
			Total int `json:"totalTokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(m.Metadata, &meta); err != nil || meta.Usage.Total != 6 {
		t.Fatalf("metadata = %s err = %v", m.Metadata, err)
	}
	if upstreamSawModel != "mock-model" || upstreamSawContent != "ping" {
		t.Fatalf("upstream saw model=%q content=%q", upstreamSawModel, upstreamSawContent)
	}
}

func TestA2ASendStreamingTaskLifecycle(t *testing.T) {
	var upstreamSawStream bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamSawStream = strings.Contains(string(b), `"stream":true`)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, streamUpstreamBody()) // "He"+"llo" + stop
	}))
	defer upstream.Close()
	gw := newA2AGateway(t, upstream, "m")

	body := `{"jsonrpc":"2.0","id":7,"method":"SendStreamingMessage","params":{` +
		`"message":{"role":"ROLE_USER","parts":[{"text":"hi"}]}}}`
	resp := postAuthed(t, gw.URL+"/rpc", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	frames := readSSE(t, resp.Body)
	if len(frames) < 3 {
		t.Fatalf("frames = %d, want >= 3 (task + artifacts + terminal status)", len(frames))
	}
	var first struct {
		Result struct {
			Task *struct {
				ID      string `json:"id"`
				Context string `json:"contextId"`
				Status  struct {
					State string `json:"state"`
				} `json:"status"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(frames[0]), &first); err != nil {
		t.Fatalf("first frame: %v", err)
	}
	if first.Result.Task == nil || first.Result.Task.Status.State != "TASK_STATE_WORKING" {
		t.Fatalf("first frame = %s", frames[0])
	}
	var collected strings.Builder
	var last struct {
		Result struct {
			StatusUpdate *struct {
				Status struct {
					State   string `json:"state"`
					Message *struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					} `json:"message"`
				} `json:"status"`
			} `json:"statusUpdate"`
		} `json:"result"`
	}
	for _, f := range frames[1:] {
		var ev struct {
			Result struct {
				ArtifactUpdate *struct {
					Artifact struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					} `json:"artifact"`
				} `json:"artifactUpdate"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(f), &ev); err != nil {
			t.Fatalf("frame %s: %v", f, err)
		}
		if ev.Result.ArtifactUpdate != nil && len(ev.Result.ArtifactUpdate.Artifact.Parts) > 0 {
			collected.WriteString(ev.Result.ArtifactUpdate.Artifact.Parts[0].Text)
		}
		if err := json.Unmarshal([]byte(f), &last); err != nil {
			t.Fatalf("final frame: %v", err)
		}
	}
	if collected.String() != "Hello" {
		t.Fatalf("artifact text = %q", collected.String())
	}
	if !upstreamSawStream { // A2A 流式入口必须把 stream=true 传到上游线上
		t.Fatal("upstream request did not carry stream:true")
	}
	if last.Result.StatusUpdate == nil || last.Result.StatusUpdate.Status.State != "TASK_STATE_COMPLETED" {
		t.Fatalf("last frame = %s", frames[len(frames)-1])
	}
	full := last.Result.StatusUpdate.Status.Message.Parts[0].Text
	if full != "Hello" {
		t.Fatalf("final message = %q", full)
	}
}

func TestA2AUnknownMethodAndTaskNotFound(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	gw := newA2AGateway(t, upstream, "m")

	for _, tc := range []struct {
		method string
		code   int
	}{
		{"GetTask", -32001},
		{"CancelTask", -32001},
		{"ListTasks", -32004},
		{"Nonsense", -32601},
	} {
		body := `{"jsonrpc":"2.0","id":1,"method":"` + tc.method + `","params":{}}`
		if code := a2aErrCode(t, gw.URL+"/rpc", body); code != tc.code {
			t.Fatalf("%s code = %d, want %d", tc.method, code, tc.code)
		}
	}
}

func TestA2ABadParams(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	gw := newA2AGateway(t, upstream, "m")

	cases := []struct {
		name string
		body string
		code int
	}{
		{"no content", `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"role":"ROLE_USER","parts":[]}}}`, -32005},
		{"bad role", `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"role":"WAT","parts":[{"text":"x"}]}}}`, -32602},
		{"bad jsonrpc", `{"jsonrpc":"1.0","id":1,"method":"SendMessage"}`, -32600},
	}
	// 缺省模型为空：不带 metadata.model 的请求在边界显式报 -32602。
	noModel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{}`)
	}))
	defer noModel.Close()
	if code := a2aErrCode(t, newA2AGateway(t, noModel, "").URL+"/rpc",
		`{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"role":"ROLE_USER","parts":[{"text":"x"}]}}}`); code != -32602 {
		t.Fatalf("no-model code = %d, want -32602", code)
	}
	for _, tc := range cases {
		if code := a2aErrCode(t, gw.URL+"/rpc", tc.body); code != tc.code {
			t.Fatalf("%s code = %d, want %d", tc.name, code, tc.code)
		}
	}
}
