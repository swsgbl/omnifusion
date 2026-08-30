package routing

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/provider"
)

func newQuotaWithClock(t *testing.T) (*QuotaTracker, *fakeClock) {
	t.Helper()
	qt := NewQuotaTracker()
	fc := newFakeClock()
	qt.now = fc.Now
	return qt, fc
}

// TestQuotaMinuteWindowFlips 是 验收：分钟窗口翻转正确。
func TestQuotaMinuteWindowFlips(t *testing.T) {
	qt, fc := newQuotaWithClock(t)
	qt.SetLimit("p", QuotaLimits{RPM: 2})

	qt.RecordRequest("p")
	if blocked, _ := qt.Blocked("p"); blocked {
		t.Fatal("1/2 RPM must not block")
	}
	qt.RecordRequest("p")
	blocked, reason := qt.Blocked("p")
	if !blocked || !strings.Contains(reason, "rpm") {
		t.Fatalf("Blocked = %v %q, want rpm exhausted", blocked, reason)
	}

	fc.Add(quotaMinute + time.Second) // 窗口滑出
	if blocked, _ := qt.Blocked("p"); blocked {
		t.Fatal("minute window must roll over")
	}
}

// TestQuotaDayWindowFlips 验收：日窗口翻转正确，且与分钟窗口独立。
func TestQuotaDayWindowFlips(t *testing.T) {
	qt, fc := newQuotaWithClock(t)
	qt.SetLimit("p", QuotaLimits{RPM: 10, RPD: 1})

	qt.RecordRequest("p")
	blocked, reason := qt.Blocked("p")
	if !blocked || !strings.Contains(reason, "rpd") {
		t.Fatalf("Blocked = %v %q, want rpd exhausted", blocked, reason)
	}

	fc.Add(quotaMinute + time.Second) // 分钟窗滑出，日窗仍在
	if blocked, _ := qt.Blocked("p"); !blocked {
		t.Fatal("day window must still block after minute rolls")
	}
	fc.Add(quotaDay) // 累计超过 24h
	if blocked, _ := qt.Blocked("p"); blocked {
		t.Fatal("day window must roll over")
	}
}

func TestQuotaTokenWindows(t *testing.T) {
	qt, fc := newQuotaWithClock(t)
	qt.SetLimit("p", QuotaLimits{TPM: 200, TPD: 300})

	qt.RecordTokens("p", 120)
	if blocked, _ := qt.Blocked("p"); blocked {
		t.Fatal("120/200 TPM must not block")
	}
	qt.RecordTokens("p", 90)
	blocked, reason := qt.Blocked("p")
	if !blocked || !strings.Contains(reason, "tpm") {
		t.Fatalf("Blocked = %v %q, want tpm exhausted", blocked, reason)
	}

	fc.Add(quotaMinute + time.Second) // tpm 窗滑出；tpd=210/300 仍在
	if blocked, _ := qt.Blocked("p"); blocked {
		t.Fatal("tpm window must roll over")
	}
	qt.RecordTokens("p", 90) // tpd 触顶
	if blocked, reason := qt.Blocked("p"); !blocked || !strings.Contains(reason, "tpd") {
		t.Fatalf("Blocked = %v %q, want tpd exhausted", blocked, reason)
	}
	fc.Add(quotaDay)
	if blocked, _ := qt.Blocked("p"); blocked {
		t.Fatal("tpd window must roll over")
	}
}

func TestQuotaUnlimitedAndOtherKeysNeverBlock(t *testing.T) {
	qt, _ := newQuotaWithClock(t)
	qt.SetLimit("limited", QuotaLimits{RPM: 1})
	qt.RecordRequest("limited")
	qt.RecordRequest("limited")

	if blocked, _ := qt.Blocked("unlimited"); blocked {
		t.Error("keys without limits must never block")
	}
	if blocked, _ := qt.Blocked("other-limited"); blocked {
		t.Error("limits must be per key")
	}
	if blocked, _ := qt.Blocked("limited"); !blocked {
		t.Error("limited key must block")
	}
}

func TestQuotaUsageSnapshot(t *testing.T) {
	qt, fc := newQuotaWithClock(t)
	qt.RecordRequest("p")
	qt.RecordRequest("p")
	qt.RecordTokens("p", 30)
	fc.Add(quotaMinute + 30*time.Second)
	qt.RecordRequest("p")
	qt.RecordTokens("p", 20)

	rpm, rpd, tpm, tpd := qt.Usage("p")
	if rpm != 1 || rpd != 3 {
		t.Errorf("requests: rpm=%d rpd=%d, want 1/3", rpm, rpd)
	}
	if tpm != 20 || tpd != 50 {
		t.Errorf("tokens: tpm=%d tpd=%d, want 20/50", tpm, tpd)
	}
}

// TestQuotaSnapshots 验证 Dashboard 读 API：设限与仅有用量的
// key 都出现在快照里，字段取值正确，无流量 key 不占位。
func TestQuotaSnapshots(t *testing.T) {
	qt, _ := newQuotaWithClock(t)
	qt.SetLimit("groq", QuotaLimits{RPM: 30, RPD: 100})
	qt.RecordRequest("groq")
	qt.RecordRequest("groq")
	qt.RecordTokens("groq", 120)
	qt.RecordRequest("ollama") // 未设限但有流量：也要可见

	sn := qt.Snapshots()
	if len(sn) != 2 {
		t.Fatalf("snapshot keys = %d (%v), want 2", len(sn), sn)
	}
	g := sn["groq"]
	if g.RPM != 2 || g.RPD != 2 || g.TPM != 120 || g.TPD != 120 {
		t.Errorf("groq usage = %+v, want rpm/rpd=2 tpm/tpd=120", g)
	}
	if g.Limits.RPM != 30 || g.Limits.RPD != 100 || g.Limits.TPM != 0 {
		t.Errorf("groq limits = %+v, want rpm=30 rpd=100 tpm=0", g.Limits)
	}
	if g.Headroom <= 0.9 || g.Headroom > 1 {
		t.Errorf("groq headroom = %v, want ~0.933", g.Headroom)
	}
	o := sn["ollama"]
	if o.RPM != 1 || o.Limits.RPM != 0 {
		t.Errorf("ollama = %+v, want rpm=1 no limits", o)
	}
	if o.Headroom != 1 {
		t.Errorf("ollama headroom = %v, want 1 (unlimited)", o.Headroom)
	}
}

// TestDispatchSkipsQuotaExhausted 验证 Router 集成：RPM=1 的 provider
// 打满后，下一请求跳过它（记 skip），由次选服务。
func TestDispatchSkipsQuotaExhausted(t *testing.T) {
	hits := map[string]int{}
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits["a"]++
		io.WriteString(w, okCompletion("model-a"))
	}))
	defer upA.Close()
	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits["b"]++
		io.WriteString(w, okCompletion("model-b"))
	}))
	defer upB.Close()

	qt := NewQuotaTracker()
	qt.SetLimit("a", QuotaLimits{RPM: 1})
	r := &Router{
		Providers: []provider.Provider{
			newMockAdapter(t, "a", upA.URL),
			newMockAdapter(t, "b", upB.URL),
		},
		Quota: qt,
	}

	if _, _, err := r.Dispatch(context.Background(), testRequest()); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if hits["a"] != 1 {
		t.Fatalf("first dispatch must hit a (hits=%v)", hits)
	}

	resp, attempts, err := r.Dispatch(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	if resp.ProviderName != "b" {
		t.Errorf("second dispatch provider = %q", resp.ProviderName)
	}
	if hits["a"] != 1 {
		t.Errorf("quota-exhausted provider must not be hit again (hits=%v)", hits)
	}
	if len(attempts) != 2 || attempts[0].Err == nil || !strings.Contains(attempts[0].Err.Error(), "quota") {
		t.Fatalf("attempts = %+v, want quota skip record first", attempts)
	}
	_, _, tpm, _ := qt.Usage("a")
	_ = tpm // okCompletion 无 usage 字段：token 记账为 0 属预期
}

// TestDispatchRecordsResponseUsage 验证非流式成功后按 resp.usage 记 token。
func TestDispatchRecordsResponseUsage(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{
			"id":"c1","object":"chat.completion","created":1,"model":"m",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}
		}`)
	}))
	defer up.Close()

	qt := NewQuotaTracker()
	qt.SetLimit("a", QuotaLimits{TPM: 7})
	r := &Router{Providers: []provider.Provider{newMockAdapter(t, "a", up.URL)}, Quota: qt}

	if _, _, err := r.Dispatch(context.Background(), testRequest()); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if blocked, reason := qt.Blocked("a"); !blocked || !strings.Contains(reason, "tpm") {
		t.Fatalf("Blocked = %v %q, want tpm exhausted from recorded usage", blocked, reason)
	}
	_, _, tpm, _ := qt.Usage("a")
	if tpm != 7 {
		t.Errorf("tpm = %d, want 7", tpm)
	}
}

// TestDispatchStreamRecordsTokensOnClose 验证流式路径：流尾 usage chunk
// 在 Close 时补记进 token 窗口。
func TestDispatchStreamRecordsTokensOnClose(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sseBody(
			chunkPayload("he"),
			`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`,
		))
	}))
	defer up.Close()

	qt := NewQuotaTracker()
	qt.SetLimit("a", QuotaLimits{TPM: 10})
	r := &Router{Providers: []provider.Provider{newMockAdapter(t, "a", up.URL)}, Quota: qt}

	stream, _, err := r.DispatchStream(context.Background(), streamRequest())
	if err != nil {
		t.Fatalf("DispatchStream: %v", err)
	}
	for { // 手工排空，不用 collectStream（其内部会 Close）
		_, err := stream.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
	}
	if _, _, tpm, _ := qt.Usage("a"); tpm != 0 {
		t.Fatalf("tokens before Close = %d, want 0", tpm)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, _, tpm, _ := qt.Usage("a"); tpm != 5 {
		t.Errorf("tpm after Close = %d, want 5", tpm)
	}
}
