package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/a2a"
	"github.com/swsgbl/omnifusion/internal/agent"
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

// newA2AGatewayTasks 是带持久任务面的夹具变体（蓝图 Phase 7）：
// 返回网关 + 注入的 TaskStore（断言任务落库用）。
func newA2AGatewayTasks(t *testing.T, upstream *httptest.Server, defaultModel string) (*httptest.Server, *agent.TaskStore) {
	t.Helper()
	adapter, err := openai_compat.New(openai_compat.Spec{
		ProviderName: "mock", BaseURL: upstream.URL + "/v1", APIKey: "sk-test",
	})
	if err != nil {
		t.Fatalf("openai_compat.New: %v", err)
	}
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	s.SetRouter(&routing.Router{Providers: []provider.Provider{adapter}})
	s.SetA2A(a2a.BuildCard(a2a.CardOptions{
		BaseURL: "http://gw.test", Version: "test", DefaultModel: defaultModel, Streaming: true,
	}), defaultModel)
	ts := agent.NewTaskStore()
	s.SetA2ATasks(ts)
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	return gw, ts
}

// streamA2A 打开一次流式请求，读完全部帧，返回（首帧任务 ID，帧集）。
func streamA2A(t *testing.T, gwURL string) (string, []string) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":9,"method":"SendStreamingMessage","params":{` +
		`"message":{"role":"ROLE_USER","parts":[{"text":"hi"}]}}}`
	resp := postAuthed(t, gwURL+"/rpc", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	frames := readSSE(t, resp.Body)
	var first struct {
		Result struct {
			Task *struct {
				ID string `json:"id"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(frames[0]), &first); err != nil || first.Result.Task == nil {
		t.Fatalf("first frame is not a task event: %v %s", err, frames[0])
	}
	return first.Result.Task.ID, frames
}

// getTaskA2A 调 GetTask 并解码任务对象。
func getTaskA2A(t *testing.T, gwURL, id string) (a2a.Task, int) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":10,"method":"GetTask","params":{"id":"` + id + `"}}`
	resp := postAuthed(t, gwURL+"/rpc", body)
	defer resp.Body.Close()
	var env struct {
		Result *a2a.Task `json:"result"`
		Error  *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("GetTask decode: %v", err)
	}
	if env.Error != nil {
		return a2a.Task{}, env.Error.Code
	}
	if env.Result == nil {
		t.Fatalf("GetTask returned neither result nor error")
	}
	return *env.Result, 0
}

func TestA2ADurableTaskLifecycle(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, streamUpstreamBody()) // "He"+"llo" + stop
	}))
	defer upstream.Close()
	gw, ts := newA2AGatewayTasks(t, upstream, "m")

	taskID, frames := streamA2A(t, gw.URL)
	if len(frames) < 3 {
		t.Fatalf("frames = %d", len(frames))
	}
	// 任务 ID 来自持久存储（t_ 前缀）。
	if !strings.HasPrefix(taskID, "t_") {
		t.Fatalf("task id %q must come from the task store", taskID)
	}
	got, code := getTaskA2A(t, gw.URL, taskID)
	if code != 0 {
		t.Fatalf("GetTask code = %d", code)
	}
	if got.Status.State != "TASK_STATE_COMPLETED" {
		t.Fatalf("GetTask state = %s", got.Status.State)
	}
	if len(got.Artifacts) != 1 || got.Artifacts[0].Parts[0].Text != "Hello" {
		t.Fatalf("GetTask artifacts = %+v", got.Artifacts)
	}
	// 存储侧同一事实。
	if st, ok := ts.Get(taskID); !ok || st.Status != agent.TaskStatusCompleted || st.Result != "Hello" {
		t.Fatalf("store task = %+v ok=%v", st, ok)
	}
	// ListTasks 包含该任务。
	list := postAuthed(t, gw.URL+"/rpc", `{"jsonrpc":"2.0","id":11,"method":"ListTasks"}`)
	defer list.Body.Close()
	var env struct {
		Result struct {
			Tasks []a2a.Task `json:"tasks"`
		} `json:"result"`
	}
	if err := json.NewDecoder(list.Body).Decode(&env); err != nil {
		t.Fatalf("ListTasks decode: %v", err)
	}
	if len(env.Result.Tasks) != 1 || env.Result.Tasks[0].ID != taskID {
		t.Fatalf("ListTasks = %+v", env.Result.Tasks)
	}
	// 终态不可取消。
	if code := a2aErrCode(t, gw.URL+"/rpc",
		`{"jsonrpc":"2.0","id":12,"method":"CancelTask","params":{"id":"`+taskID+`"}}`); code != -32002 {
		t.Fatalf("CancelTask(completed) code = %d, want -32002", code)
	}
	// 未知任务。
	if code := a2aErrCode(t, gw.URL+"/rpc",
		`{"jsonrpc":"2.0","id":13,"method":"GetTask","params":{"id":"t_missing"}}`); code != -32001 {
		t.Fatalf("GetTask(unknown) code = %d, want -32001", code)
	}
}

func TestA2ASubscribeSnapshot(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, streamUpstreamBody())
	}))
	defer upstream.Close()
	gw, _ := newA2AGatewayTasks(t, upstream, "m")

	taskID, _ := streamA2A(t, gw.URL)
	resp := postAuthed(t, gw.URL+"/rpc",
		`{"jsonrpc":"2.0","id":14,"method":"SubscribeToTask","params":{"id":"`+taskID+`"}}`)
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("subscribe content-type = %q", ct)
	}
	frames := readSSE(t, resp.Body)
	if len(frames) != 1 {
		t.Fatalf("snapshot subscribe must emit exactly one frame, got %d", len(frames))
	}
	if !strings.Contains(frames[0], "TASK_STATE_COMPLETED") || !strings.Contains(frames[0], taskID) {
		t.Fatalf("subscribe frame = %s", frames[0])
	}
}

func TestA2ACancelTaskInterruptsActiveStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"He"}}]}`+"\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done() // 挂住：网关 CancelTask 取消下游时这里解除
	}))
	defer upstream.Close()
	gw, ts := newA2AGatewayTasks(t, upstream, "m")

	type streamResult struct {
		frames  []string
		readErr bool
	}
	done := make(chan streamResult, 1)
	go func() {
		body := `{"jsonrpc":"2.0","id":20,"method":"SendStreamingMessage","params":{` +
			`"message":{"role":"ROLE_USER","parts":[{"text":"hi"}]}}}`
		req, err := http.NewRequest(http.MethodPost, gw.URL+"/rpc", strings.NewReader(body))
		if err != nil {
			done <- streamResult{readErr: true}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+testGatewayToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- streamResult{readErr: true}
			return
		}
		defer resp.Body.Close()
		frames := readSSE(t, resp.Body)
		done <- streamResult{frames: frames}
	}()

	// 等任务进入存储（working）。
	var taskID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, tsk := range ts.List("a2a", 10) {
			taskID = tsk.ID
			break
		}
		if taskID != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if taskID == "" {
		t.Fatal("task never entered the store")
	}

	resp := postAuthed(t, gw.URL+"/rpc",
		`{"jsonrpc":"2.0","id":21,"method":"CancelTask","params":{"id":"`+taskID+`"}}`)
	var env struct {
		Result *a2a.Task `json:"result"`
		Error  *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&env)
	resp.Body.Close()
	if env.Error != nil || env.Result == nil || env.Result.Status.State != "TASK_STATE_CANCELED" {
		t.Fatalf("CancelTask result = %+v err=%+v", env.Result, env.Error)
	}

	res := <-done // 流随取消收束（readSSE 在连接关闭后返回已读帧）。
	if len(res.frames) == 0 {
		t.Fatal("stream produced no frames before cancel")
	}
	if st, ok := ts.Get(taskID); !ok || st.Status != agent.TaskStatusCanceled {
		t.Fatalf("store task after cancel = %+v ok=%v", st, ok)
	}
}
