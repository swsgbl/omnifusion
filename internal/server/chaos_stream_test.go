// chaos_stream_test.go 是蓝图 Phase 10 的流式混沌门禁：截断流
// （首块后断/首块前断）与慢首 token——不变量与转发层契约对齐：
// 首块后断流=合成收尾+[DONE]（客户端永不悬挂）；首块前断=路由层
// 切换到健康备选；慢首 token=成功且 TTFT 有界。
package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
)

// sseChunk 构造一条 OpenAI 兼容增量帧。
func sseChunk(content string, finish string) string {
	fr := `"finish_reason":null`
	if finish != "" {
		fr = `"finish_reason":"` + finish + `"`
	}
	return `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"model-a","choices":[{"index":0,"delta":{"content":"` + content + `"},` + fr + `}]}` + "\n\n"
}

// chaosStreamUpstream 构造流式故障上游：mode 决定行为。
func chaosStreamUpstream(t *testing.T, mode string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		switch mode {
		case "truncate_mid": // 首块后骤断（无 [DONE]）——"cannot un-ship bytes" 面
			io.WriteString(w, sseChunk("He", ""))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			panic(http.ErrAbortHandler) // 模拟连接中途断开
		case "close_before_first": // 未给任何有效块即断——路由层首块切换面
			panic(http.ErrAbortHandler)
		case "slow_first_token":
			time.Sleep(100 * time.Millisecond)
			io.WriteString(w, sseChunk("He", ""))
			io.WriteString(w, sseChunk("llo", "stop"))
			io.WriteString(w, "data: [DONE]\n\n")
		default: // healthy
			io.WriteString(w, sseChunk("ok", "stop"))
			io.WriteString(w, "data: [DONE]\n\n")
		}
	}))
}

// TestChaosStreamTruncatedMidStream：唯一上游首块后断——客户端必须
// 拿到优雅收尾（[DONE] 结尾），不悬挂（readSSE 在连接关闭后返回）。
func TestChaosStreamTruncatedMidStream(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos suite skipped in -short")
	}
	up := chaosStreamUpstream(t, "truncate_mid")
	t.Cleanup(up.Close)
	bad, err := openai_compat.New(openai_compat.Spec{ProviderName: "bad", BaseURL: up.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	gw := newStreamGateway(t, bad)

	done := make(chan []string, 1)
	go func() {
		resp := postStream(t, gw.URL)
		defer resp.Body.Close()
		done <- readSSE(t, resp.Body)
	}()
	select {
	case frames := <-done:
		if len(frames) == 0 {
			t.Fatal("no frames before truncation close")
		}
		if frames[len(frames)-1] != "[DONE]" {
			t.Fatalf("truncated stream must close with [DONE], last=%q", frames[len(frames)-1])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("truncated stream hung (no graceful close within 5s)")
	}
}

// TestChaosStreamFailoverBeforeFirstChunk：坏上游未出块即断 + 健康
// 备选——路由层首块缓冲切换生效，客户端拿到完整流。
func TestChaosStreamFailoverBeforeFirstChunk(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos suite skipped in -short")
	}
	badUp := chaosStreamUpstream(t, "close_before_first")
	t.Cleanup(badUp.Close)
	goodUp := chaosStreamUpstream(t, "healthy")
	t.Cleanup(goodUp.Close)

	bad, _ := openai_compat.New(openai_compat.Spec{ProviderName: "bad", BaseURL: badUp.URL + "/v1", APIKey: "k"})
	good, err := openai_compat.New(openai_compat.Spec{ProviderName: "good", BaseURL: goodUp.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("good adapter: %v", err)
	}
	gw := newStreamGateway(t, bad, good)

	resp := postStream(t, gw.URL)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (first-chunk failover)", resp.StatusCode)
	}
	frames := readSSE(t, resp.Body)
	var content strings.Builder
	for _, f := range frames {
		if strings.Contains(f, `"content":"`) {
			start := strings.Index(f, `"content":"`) + len(`"content":"`)
			end := strings.Index(f[start:], `"`)
			content.WriteString(f[start : start+end])
		}
	}
	if content.String() != "ok" {
		t.Fatalf("failover stream content = %q, want \"ok\"", content.String())
	}
	if len(frames) == 0 || frames[len(frames)-1] != "[DONE]" {
		t.Fatalf("stream must end with [DONE], frames=%v", frames)
	}
}

// TestChaosStreamSlowFirstToken：首 token 延迟 100ms——成功且客户端
// TTFT 有界（<2s，本地 mock 宽松界）。
func TestChaosStreamSlowFirstToken(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos suite skipped in -short")
	}
	up := chaosStreamUpstream(t, "slow_first_token")
	t.Cleanup(up.Close)
	a, err := openai_compat.New(openai_compat.Spec{ProviderName: "slow", BaseURL: up.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	gw := newStreamGateway(t, a)

	start := time.Now()
	resp := postStream(t, gw.URL)
	defer resp.Body.Close()
	frames := readSSE(t, resp.Body)
	ttft := time.Since(start)
	if len(frames) == 0 || frames[len(frames)-1] != "[DONE]" {
		t.Fatalf("slow-first-token stream incomplete: %v", frames)
	}
	if ttft > 2*time.Second {
		t.Fatalf("TTFT = %v, want < 2s", ttft)
	}
}
