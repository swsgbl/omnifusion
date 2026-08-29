package server

import (
	"bufio"
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

func streamUpstreamBody() string {
	return "" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"He\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"llo\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
}

func newStreamGateway(t *testing.T, providers ...provider.Provider) *httptest.Server {
	t.Helper()
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	s.SetRouter(&routing.Router{Providers: providers})
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	return gw
}

// readSSE 收集全部 "data:" 帧（原样，不去 [DONE]）。
func readSSE(t *testing.T, body io.Reader) []string {
	t.Helper()
	var frames []string
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data: ") {
			frames = append(frames, strings.TrimPrefix(line, "data: "))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return frames
}

func postStream(t *testing.T, gwURL string) *http.Response {
	t.Helper()
	return postAuthed(t, gwURL+"/v1/chat/completions",
		`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
}

func TestChatCompletionsStreamingE2E(t *testing.T) {
	var upstreamSawStream string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamSawStream = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, streamUpstreamBody())
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
	gw := newStreamGateway(t, adapter)

	resp := postStream(t, gw.URL)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	if !strings.Contains(upstreamSawStream, `"stream":true`) {
		t.Errorf("upstream body lost stream flag: %s", upstreamSawStream)
	}

	frames := readSSE(t, resp.Body)
	if len(frames) != 4 { // 3 chunks + [DONE]
		t.Fatalf("frames = %d, want 4: %v", len(frames), frames)
	}
	if frames[len(frames)-1] != "[DONE]" {
		t.Errorf("last frame = %q, want [DONE]", frames[len(frames)-1])
	}
	var text strings.Builder
	var finish string
	for _, f := range frames[:len(frames)-1] {
		var chunk schema.Chunk
		if err := json.Unmarshal([]byte(f), &chunk); err != nil {
			t.Fatalf("frame %q not a chunk: %v", f, err)
		}
		if chunk.Object != "chat.completion.chunk" {
			t.Errorf("object = %q", chunk.Object)
		}
		for _, c := range chunk.Choices {
			text.WriteString(c.Delta.Content.TextOf())
			if c.FinishReason != "" {
				finish = c.FinishReason
			}
		}
	}
	if text.String() != "Hello" {
		t.Errorf("reassembled text = %q", text.String())
	}
	if finish != "stop" {
		t.Errorf("finish_reason = %q", finish)
	}
}

func TestChatCompletionsStreamingFailover(t *testing.T) {
	// buffer-first-chunk：首家对流式请求 500，客户端仍应拿到 200 SSE。
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"boom"}`)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, streamUpstreamBody())
	}))
	defer good.Close()

	badAdapter, err := openai_compat.New(openai_compat.Spec{ProviderName: "bad", BaseURL: bad.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("New(bad): %v", err)
	}
	goodAdapter, err := openai_compat.New(openai_compat.Spec{ProviderName: "good", BaseURL: good.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("New(good): %v", err)
	}
	gw := newStreamGateway(t, badAdapter, goodAdapter)

	resp := postStream(t, gw.URL)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	frames := readSSE(t, resp.Body)
	if len(frames) != 4 || frames[len(frames)-1] != "[DONE]" {
		t.Fatalf("frames = %v", frames)
	}
}

func TestChatCompletionsStreamingMidStreamBreak(t *testing.T) {
	// M3.4 验收：首帧落地后上游断流——已发出的 200 与首帧保持，
	// 客户端收到合成 finish 帧与 [DONE] 的优雅收尾，而非悬挂连接。
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("no hijacker")
			return
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		rw.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\n")
		rw.WriteString("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"part\"},\"finish_reason\":null}]}\n\n")
		rw.Flush()
	}))
	defer broken.Close()

	adapter, err := openai_compat.New(openai_compat.Spec{ProviderName: "broken", BaseURL: broken.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("openai_compat.New: %v", err)
	}
	gw := newStreamGateway(t, adapter)

	resp := postStream(t, gw.URL)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	frames := readSSE(t, resp.Body)
	if len(frames) != 3 { // 已发首帧 + 合成 finish + [DONE]
		t.Fatalf("frames = %v, want 3 (shipped chunk, synthetic finish, [DONE])", frames)
	}
	if !strings.Contains(frames[0], `"part"`) {
		t.Errorf("first frame lost: %q", frames[0])
	}
	var tailChunk schema.Chunk
	if err := json.Unmarshal([]byte(frames[1]), &tailChunk); err != nil {
		t.Fatalf("synthetic frame not a chunk: %v", err)
	}
	if len(tailChunk.Choices) != 1 || tailChunk.Choices[0].FinishReason != schema.FinishStop ||
		tailChunk.Choices[0].Index != 0 || tailChunk.ID != "c1" || tailChunk.Model != "m" {
		t.Errorf("synthetic finish = %+v", tailChunk.Choices)
	}
	if frames[2] != "[DONE]" {
		t.Errorf("last frame = %q, want [DONE]", frames[2])
	}
}
