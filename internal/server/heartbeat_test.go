// heartbeat_test.go 覆盖下游 SSE 心跳：上游思考静默期间网关周期性
// 下发注释保活帧（OpenAI 线路）/ ping 事件（Anthropic 线路），数据
// 帧不受影响、终态语义不变。
package server

import (
	"bufio"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
)

// TestChatStreamKeepaliveDuringUpstreamSilence 上游首帧后静默 180ms：
// 间隔缩短到 40ms 后客户端必须收到注释心跳帧，且数据帧与 [DONE] 完整。
func TestChatStreamKeepaliveDuringUpstreamSilence(t *testing.T) {
	old := sseKeepaliveInterval
	sseKeepaliveInterval = 40 * time.Millisecond
	t.Cleanup(func() { sseKeepaliveInterval = old })

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"He\"}}]}\n\n")
		flusher.Flush()
		time.Sleep(180 * time.Millisecond) // 思考静默
		io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	adapter, err := openai_compat.New(openai_compat.Spec{
		ProviderName: "slow-thinker",
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
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var comments, dataFrames int
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, ": omnifusion-keepalive") {
			comments++
		} else if strings.HasPrefix(line, "data: ") {
			dataFrames++
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if comments == 0 {
		t.Error("no keepalive comment frames during 180ms upstream silence")
	}
	if dataFrames != 3 { // He + finish + [DONE]
		t.Errorf("data frames = %d, want 3", dataFrames)
	}
}

// TestMessagesStreamPingDuringUpstreamSilence Anthropic 线路：静默期
// 下发官方 event: ping 帧，收尾事件序列不受影响。
func TestMessagesStreamPingDuringUpstreamSilence(t *testing.T) {
	old := sseKeepaliveInterval
	sseKeepaliveInterval = 40 * time.Millisecond
	t.Cleanup(func() { sseKeepaliveInterval = old })

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"}}]}\n\n")
		flusher.Flush()
		time.Sleep(150 * time.Millisecond)
		io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	adapter, err := openai_compat.New(openai_compat.Spec{
		ProviderName: "slow-claude",
		BaseURL:      upstream.URL + "/v1",
		APIKey:       "k",
	})
	if err != nil {
		t.Fatalf("openai_compat.New: %v", err)
	}
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	s.SetRouter(&routing.Router{Providers: []provider.Provider{adapter}})
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)

	resp := postAuthed(t, gw.URL+"/v1/messages",
		`{"model":"m","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var pings int
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "event: ping") {
			pings++
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if pings == 0 {
		t.Error("no ping events during 150ms upstream silence")
	}
}
