// 进程内 mock 上游：行为对齐 scripts/mockup（只读参照，不改动）——
// GET /v1/models 返回单模型目录；POST /v1/chat/completions 任意模型名
// 均接受，流式 3 chunk + data: [DONE]，chunk 间隔 2ms 与 mockup 一致。
// 必须绑 127.0.0.1:11434：网关内置 ollama provider 硬编码该地址。
package bench_test

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

var mockReqs atomic.Int64 // 上游累计请求数，用于注入回复序号

// startMockUpstream 在探测好的 listener 上服务 mock 上游。
func startMockUpstream(ln net.Listener) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", handleMockModels)
	mux.HandleFunc("/v1/chat/completions", handleMockChat)
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	return srv
}

// handleMockModels 返回 OpenAI 兼容的模型目录（仅 bench-model）。
func handleMockModels(w http.ResponseWriter, _ *http.Request) {
	writeMockJSON(w, map[string]any{
		"object": "list",
		"data": []map[string]any{{
			"id": benchModel, "object": "model", "owned_by": "mockup",
		}},
	})
}

type mockChatRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

// handleMockChat 实现非流式 JSON 与流式 SSE 两种应答，任意模型名均接受。
func handleMockChat(w http.ResponseWriter, r *http.Request) {
	var req mockChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":{"message":"bad json"}}`, http.StatusBadRequest)
		return
	}
	var prompt strings.Builder
	for _, m := range req.Messages {
		if m.Role == "user" {
			prompt.WriteString(m.Content)
		}
	}
	n := mockReqs.Add(1)
	reply := fmt.Sprintf("mock reply #%d to %q from %s", n, prompt.String(), req.Model)
	if req.Stream {
		writeMockSSE(w, reply)
		return
	}
	writeMockJSON(w, map[string]any{
		"id":      fmt.Sprintf("chatcmpl-mock-%d", n),
		"object":  "chat.completion",
		"model":   req.Model,
		"choices": []map[string]any{{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": reply}}},
		"usage":   map[string]any{"prompt_tokens": 9, "completion_tokens": 18, "total_tokens": 27},
	})
}

// writeMockSSE 输出 3 个 chunk 后以 [DONE] 收尾；chunk 间隔 2ms 对齐
// scripts/mockup 的 -chunk-delay 默认值。
func writeMockSSE(w http.ResponseWriter, reply string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	id := fmt.Sprintf("chatcmpl-mock-%d", mockReqs.Load())
	chunks := []string{
		mockSSEData(map[string]any{"id": id, "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant"}}}}),
		mockSSEData(map[string]any{"id": id, "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": reply}}}}),
		mockSSEData(map[string]any{"id": id, "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}}),
	}
	for _, c := range chunks {
		fmt.Fprint(w, c)
		fl.Flush()
		time.Sleep(2 * time.Millisecond)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	fl.Flush()
}

// mockSSEData 把任意值编成一行 SSE data: 帧。
func mockSSEData(v any) string {
	b, _ := json.Marshal(v)
	return "data: " + string(b) + "\n\n"
}

// writeMockJSON 输出 JSON 应答。
func writeMockJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
