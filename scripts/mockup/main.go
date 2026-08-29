// mockup — OpenAI 兼容的最小 mock 上游（约 25 QPS 假负载，零依赖）。
//
// 用途：无真实 API key 环境下的全链路验证——
//   - 冒烟脚本 scripts/smoke.{sh,ps1} 的上游
//   - Docker compose `mock` profile 的 sidecar（与网关共享网络命名空间，
//     使 ollama provider 的硬编码 http://localhost:11434/v1 指向本 mock）
//
// 监听 127.0.0.1:11434 时，网关内置 ollama provider（optional_key）自动装配。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

var (
	addr       = flag.String("addr", "127.0.0.1:11434", "监听地址")
	modelID    = flag.String("model", "mock-model-1", "暴露的模型 id")
	chunkDelay = flag.Duration("chunk-delay", 2*time.Millisecond, "流式 chunk 间隔")
	reqs       atomic.Int64
)

func main() {
	flag.Parse()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", handleModels)
	mux.HandleFunc("/v1/chat/completions", handleChat)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintln(w, `{"status":"ok"}`)
	})
	log.Printf("mockup listening on http://%s (model=%s)", *addr, *modelID)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func handleModels(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"object": "list",
		"data": []map[string]any{{
			"id": *modelID, "object": "model", "owned_by": "mockup",
		}},
	})
}

type chatRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	n := reqs.Add(1)
	var req chatRequest
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
	reply := fmt.Sprintf("mock reply #%d to %q from %s", n, prompt.String(), req.Model)
	if req.Stream {
		writeSSE(w, reply)
		return
	}
	writeJSON(w, map[string]any{
		"id":      fmt.Sprintf("chatcmpl-mock-%d", n),
		"object":  "chat.completion",
		"model":   req.Model,
		"choices": []map[string]any{{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": reply}}},
		"usage":   map[string]any{"prompt_tokens": 9, "completion_tokens": 18, "total_tokens": 27},
	})
}

func writeSSE(w http.ResponseWriter, reply string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	id := fmt.Sprintf("chatcmpl-mock-%d", reqs.Load())
	chunks := []string{
		sseData(map[string]any{"id": id, "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant"}}}}),
		sseData(map[string]any{"id": id, "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": reply}}}}),
		sseData(map[string]any{"id": id, "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}}),
	}
	for _, c := range chunks {
		_, _ = fmt.Fprint(w, c)
		fl.Flush()
		time.Sleep(*chunkDelay)
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	fl.Flush()
}

func sseData(v any) string {
	b, _ := json.Marshal(v)
	return "data: " + string(b) + "\n\n"
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
