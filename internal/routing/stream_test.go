package routing

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
)

func sseBody(parts ...string) string {
	var sb strings.Builder
	for _, p := range parts {
		sb.WriteString("data: ")
		sb.WriteString(p)
		sb.WriteString("\n\n")
	}
	sb.WriteString("data: [DONE]\n\n")
	return sb.String()
}

func chunkPayload(content string) string {
	return `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m",` +
		`"choices":[{"index":0,"delta":{"content":"` + content + `"},"finish_reason":null}]}`
}

func streamRequest() *schema.UnifiedRequest {
	req := testRequest()
	req.Stream = true
	return req
}

func collectStream(t *testing.T, stream provider.ChunkStream) []string {
	t.Helper()
	defer stream.Close()
	var out []string
	for {
		chunk, err := stream.Next(context.Background())
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		for _, c := range chunk.Choices {
			out = append(out, c.Delta.Content.TextOf())
		}
	}
}

func TestDispatchStreamFallsBackBeforeFirstChunk(t *testing.T) {
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, `{"error":"melting"}`)
	}))
	defer upA.Close()
	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sseBody(chunkPayload("hello")))
	}))
	defer upB.Close()

	r := &Router{Providers: []provider.Provider{
		newMockAdapter(t, "flaky", upA.URL),
		newMockAdapter(t, "steady", upB.URL),
	}}
	stream, attempts, err := r.DispatchStream(context.Background(), streamRequest())
	if err != nil {
		t.Fatalf("DispatchStream: %v", err)
	}
	got := collectStream(t, stream)
	if len(got) != 1 || got[0] != "hello" {
		t.Errorf("chunks = %v", got)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(attempts))
	}
	if attempts[0].Err == nil {
		t.Error("first attempt should record an error")
	}
}

func TestDispatchStreamEmptyBodyFailsOver(t *testing.T) {
	// 200 + empty body: no first chunk may be buffered, so the router
	// must treat it as failure and move on (buffer-first-chunk).
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer upA.Close()
	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, sseBody(chunkPayload("x")))
	}))
	defer upB.Close()

	r := &Router{Providers: []provider.Provider{
		newMockAdapter(t, "silent", upA.URL),
		newMockAdapter(t, "steady", upB.URL),
	}}
	stream, attempts, err := r.DispatchStream(context.Background(), streamRequest())
	if err != nil {
		t.Fatalf("DispatchStream: %v", err)
	}
	got := collectStream(t, stream)
	if len(got) != 1 || got[0] != "x" {
		t.Errorf("chunks = %v", got)
	}
	if len(attempts) != 2 || attempts[0].Err == nil {
		t.Errorf("attempts = %+v", attempts)
	}
}

// textOnlyProvider 实现 Provider 但不实现 StreamParser。
type textOnlyProvider struct {
	provider.Provider
	name string
}

func (p *textOnlyProvider) Name() string { return p.name }

func TestDispatchStreamSkipsNonStreamingProvider(t *testing.T) {
	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, sseBody(chunkPayload("y")))
	}))
	defer upB.Close()

	r := &Router{Providers: []provider.Provider{
		&textOnlyProvider{name: "text-only"},
		newMockAdapter(t, "streamy", upB.URL),
	}}
	stream, attempts, err := r.DispatchStream(context.Background(), streamRequest())
	if err != nil {
		t.Fatalf("DispatchStream: %v", err)
	}
	got := collectStream(t, stream)
	if len(got) != 1 || got[0] != "y" {
		t.Errorf("chunks = %v", got)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(attempts))
	}
	if !errors.Is(attempts[0].Err, ErrStreamUnsupported) {
		t.Errorf("skip reason = %v, want ErrStreamUnsupported", attempts[0].Err)
	}
}

func TestDispatchStreamAllFail(t *testing.T) {
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer upA.Close()

	r := &Router{Providers: []provider.Provider{newMockAdapter(t, "a", upA.URL)}}
	_, attempts, err := r.DispatchStream(context.Background(), streamRequest())
	var de *DispatchError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want DispatchError", err)
	}
	if len(attempts) != 1 {
		t.Errorf("attempts = %d", len(attempts))
	}
}

func TestDispatchStreamNoProviders(t *testing.T) {
	r := &Router{}
	_, _, err := r.DispatchStream(context.Background(), streamRequest())
	if err == nil {
		t.Fatal("expected error for empty provider list")
	}
}

// breakUpstream 发一个 chunk 后不带 [DONE] 直接收工：网关读侧得到
// StreamEndedWithoutDone，即"首 chunk 后断流"的注入器（验收）。
func breakUpstream(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, "data: "+chunkPayload("x")+"\n\n")
}

func TestDispatchStreamMidStreamBreakPenalized(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(breakUpstream))
	defer up.Close()

	iso, err := NewIsolation(nil, nil)
	if err != nil {
		t.Fatalf("NewIsolation: %v", err)
	}
	r := &Router{
		Providers: []provider.Provider{newMockAdapter(t, "flaky", up.URL)},
		Scoring:   NewScorer(),
		Isolation: iso,
	}

	// 第 1 次断流：DispatchStream 成功（首 chunk 落地），随后的 Next
	// 报流中断裂——不得重试切换，但要记健康降分。
	stream, _, err := r.DispatchStream(context.Background(), streamRequest())
	if err != nil {
		t.Fatalf("DispatchStream: %v", err)
	}
	if _, err := stream.Next(context.Background()); err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	if _, err := stream.Next(context.Background()); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("second Next = %v, want mid-stream break", err)
	}
	if _, succ := r.Scoring.Snapshot("flaky"); succ >= 1 {
		t.Errorf("success rate = %v, want health penalty applied", succ)
	}
	stream.Close()

	// stream_broken 计入熔断窗口：每次 dispatch 记一次首 chunk 成功 +
	// 一次断裂，5 轮断流后窗口内失败数达阈值（>=5 且失败率 >=0.5），
	// provider 被熔断拦下。
	for i := 0; i < 4; i++ { // 连同上面第 1 次共 5 次
		stream, _, err := r.DispatchStream(context.Background(), streamRequest())
		if err != nil {
			t.Fatalf("DispatchStream #%d: %v", i, err)
		}
		stream.Next(context.Background())
		stream.Next(context.Background())
		stream.Close()
	}
	if blocked, _ := iso.Block("flaky"); !blocked {
		t.Error("provider not breaker-blocked after repeated mid-stream breaks")
	}
}

func TestDispatchStreamClientCancelNotPenalized(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, sseBody(chunkPayload("y")))
	}))
	defer up.Close()

	r := &Router{
		Providers: []provider.Provider{newMockAdapter(t, "steady", up.URL)},
		Scoring:   NewScorer(),
	}
	stream, _, err := r.DispatchStream(context.Background(), streamRequest())
	if err != nil {
		t.Fatalf("DispatchStream: %v", err)
	}
	defer stream.Close()

	ctx, cancel := context.WithCancel(context.Background())
	if _, err := stream.Next(ctx); err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	cancel()
	if _, err := stream.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("second Next = %v, want context.Canceled", err)
	}
	if _, succ := r.Scoring.Snapshot("steady"); succ != 1 {
		t.Errorf("success rate = %v, want 1: client cancel is not the provider's fault", succ)
	}
}
