// chainbudget_test.go 覆盖失败链预算制（v0.1.34，用户实撞"等很久
// 最终失败"）：慢死上游链 + 快免费池 → 总时长有界、免费池胜出、
// 预算跳过留痕；无 safety 候选时行为零变化。
package routing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
)

// silentUpstream 收下请求后长期不发一字节（死连接静默体——预算制
// 要根治的最恶毒形态：header 正常、body 永不首 chunk）。返回的服务
// 在 Cleanup 时即时解锁 handler（不等满睡眠时长）。
func silentUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-done:
		case <-time.After(30 * time.Second): // 测试生命期内等同永不发字节
		}
	}))
	t.Cleanup(srv.Close)
	// LIFO：close(done) 后注册先执行，handler 即刻解锁，srv.Close 不等待。
	t.Cleanup(func() { close(done) })
	return srv
}

// budgetCap 是"这些家有能力数据（进 @quality 候选 L）"的假源。
type budgetCap map[string]string // provider → best model

func (c budgetCap) Capability(providerName, _ string) (float64, bool) {
	_, ok := c[providerName]
	return 80, ok
}
func (c budgetCap) BestModel(providerName string) (string, float64, bool) {
	m, ok := c[providerName]
	return m, 80, ok
}

// budgetRouter 构造 L 家（能力源）+ kilo 免费安全网（唯一登记免费价）。
func budgetRouter(t *testing.T, deadNames ...string) *Router {
	t.Helper()
	cap := budgetCap{}
	var provs []provider.Provider
	for _, n := range deadNames {
		s := silentUpstream(t) // 自带 Close+done 解锁注册
		cap[n] = "m-" + n
		provs = append(provs, newMockAdapter(t, n, s.URL))
	}
	free := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, okCompletion("kilo-auto/free"))
	}))
	t.Cleanup(free.Close)
	provs = append(provs, newMockAdapter(t, "kilo", free.URL))
	return &Router{
		Providers:  provs,
		Capability: cap,
		Price:      safetyPrice{"kilo": {model: "kilo-auto/free", in: 0, out: 0}},
	}
}

func shrinkBudgets(t *testing.T) {
	t.Helper()
	oldA, oldC := failoverAttemptBudget, failoverChainBudget
	failoverAttemptBudget, failoverChainBudget = 400*time.Millisecond, 900*time.Millisecond
	t.Cleanup(func() { failoverAttemptBudget, failoverChainBudget = oldA, oldC })
}

// TestChainBudgetJumpsToFreeLane 非流：四家静默死上游（无预算时每家
// 吃满上游超时）→ 单次预算 400ms/家掐断、链预算 900ms 后跳过剩余
// 非 safety 候选 → kilo 胜出，链上留 chain_budget 证据。
func TestChainBudgetJumpsToFreeLane(t *testing.T) {
	shrinkBudgets(t)
	r := budgetRouter(t, "dead-a", "dead-b", "dead-c", "dead-d")
	req := &schema.UnifiedRequest{Model: "", Messages: []schema.Message{
		{Role: "user", Content: schema.NewTextContent("hi")},
	}}
	start := time.Now()
	resp, attempts, err := r.Dispatch(context.Background(), req, WithQualityAuto())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.ProviderName != "kilo" {
		t.Fatalf("winner = %s, want kilo", resp.ProviderName)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("elapsed = %v, want bounded <3s (budget not effective)", elapsed)
	}
	skipped := 0
	for _, a := range attempts {
		if a.SkipReason == "chain_budget" {
			skipped++
		}
	}
	if skipped == 0 {
		t.Fatalf("no chain_budget skip recorded: %+v", attempts)
	}
}

// TestChainBudgetStreamFirstChunk 流式：静默上游的首 chunk 被预算掐断
//（不吃满 300s 流超时），链预算后免费池以流式胜出——且长流不受影响。
func TestChainBudgetStreamFirstChunk(t *testing.T) {
	shrinkBudgets(t)
	sse := "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"
	free := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse)
	}))
	t.Cleanup(free.Close)
	silent := silentUpstream(t) // 自带 Close+done 解锁注册
	cap := budgetCap{"dead-a": "m"}
	r := &Router{
		Providers: []provider.Provider{
			newMockAdapter(t, "dead-a", silent.URL),
			newMockAdapter(t, "kilo", free.URL),
		},
		Capability: cap,
		Price:      safetyPrice{"kilo": {model: "kilo-auto/free", in: 0, out: 0}},
	}
	req := &schema.UnifiedRequest{Model: "", Stream: true, Messages: []schema.Message{
		{Role: "user", Content: schema.NewTextContent("hi")},
	}}
	start := time.Now()
	stream, _, err := r.DispatchStream(context.Background(), req, WithQualityAuto())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("DispatchStream: %v", err)
	}
	defer stream.Close()
	if elapsed > 3*time.Second {
		t.Fatalf("elapsed = %v, want bounded <3s", elapsed)
	}
	first, err := stream.Next(context.Background())
	if err != nil || first == nil || len(first.Choices) != 1 {
		t.Fatalf("first chunk = %+v err=%v", first, err)
	}
	if first.Choices[0].Delta.Content.TextOf() != "ok" {
		t.Fatalf("first chunk text = %q", first.Choices[0].Delta.Content.TextOf())
	}
}

// TestNoSafetyLaneKeepsBudgetsOff 无 safety 候选（普通裸模型请求）时
// 预算线不生效：慢上游按原有超时语义等待（这里静默 1.2s 后正常应答
// ——预算若误生效会在 400ms 掐断它）。
func TestNoSafetyLaneKeepsBudgetsOff(t *testing.T) {
	shrinkBudgets(t)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1200 * time.Millisecond)
		io.WriteString(w, okCompletion("m"))
	}))
	t.Cleanup(slow.Close)
	r := &Router{Providers: []provider.Provider{newMockAdapter(t, "only", slow.URL)}}
	resp, _, err := r.Dispatch(context.Background(),
		&schema.UnifiedRequest{Model: "m", Messages: []schema.Message{
			{Role: "user", Content: schema.NewTextContent("hi")},
		}})
	if err != nil {
		t.Fatalf("Dispatch must not apply budgets without a safety lane: %v", err)
	}
	if resp.ProviderName != "only" {
		t.Fatalf("winner = %s", resp.ProviderName)
	}
}
