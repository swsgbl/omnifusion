package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
)

// TestStreamingTTFTOverhead 验收 docs/05 M1.5：流式首包时间与直连上游
// 的差 < 5ms（本机基准）。用本地 mock 上游隔离网络波动，只测网关自身
// 开销（路由 + 翻译 + SSE 转发）。两侧都以收到首个 "data:" 字节为准。
func TestStreamingTTFTOverhead(t *testing.T) {
	payload := "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"},\"finish_reason\":null}]}\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, payload)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	adapter, err := openai_compat.New(openai_compat.Spec{
		ProviderName: "mock",
		BaseURL:      upstream.URL,
	})
	if err != nil {
		t.Fatalf("openai_compat.New: %v", err)
	}
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	s.SetRouter(&routing.Router{Providers: []provider.Provider{adapter}})
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()

	reqBody := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	// authed=true 时带网关 key（打网关）；直连上游时不带（打 mock）。
	ttft := func(url string, authed bool) time.Duration {
		start := time.Now()
		req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(reqBody))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if authed {
			req.Header.Set("Authorization", "Bearer "+testGatewayToken)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", url, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		buf := make([]byte, 6)
		if _, err := io.ReadFull(resp.Body, buf); err != nil {
			t.Fatalf("read first frame: %v", err)
		}
		if string(buf) != "data: " {
			t.Fatalf("first bytes = %q, want SSE frame prefix", buf)
		}
		return time.Since(start)
	}

	const warmup, rounds = 10, 50
	for i := 0; i < warmup; i++ {
		ttft(upstream.URL, false)
		ttft(gw.URL+"/v1/chat/completions", true)
	}
	direct := make([]time.Duration, rounds)
	viaGW := make([]time.Duration, rounds)
	for i := 0; i < rounds; i++ {
		direct[i] = ttft(upstream.URL, false)
		viaGW[i] = ttft(gw.URL+"/v1/chat/completions", true)
	}
	sort.Slice(direct, func(i, j int) bool { return direct[i] < direct[j] })
	sort.Slice(viaGW, func(i, j int) bool { return viaGW[i] < viaGW[j] })
	median := func(d []time.Duration) time.Duration { return d[len(d)/2] }

	diff := median(viaGW) - median(direct)
	t.Logf("median TTFT: direct=%s gateway=%s diff=%s", median(direct), median(viaGW), diff)
	if diff > 5*time.Millisecond {
		t.Errorf("gateway streaming TTFT overhead %s exceeds 5ms acceptance", diff)
	}
}
