// safetynet_test.go 覆盖裸 auto 指令的免费安全网（2026-10-23 用户
// 实撞：keyed 免费层大面积劣化时 @quality 直接 502——管家不可用；
// 免密免费池必须兜底）。
package routing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
)

// safetyPrice 是带模型名的定价源：provider → (model, price)。
type safetyPrice map[string]safetyEntry

type safetyEntry struct {
	model string
	in    float64
	out   float64
}

func (p safetyPrice) Price(providerName, model string) (provider.Price, bool) {
	e, ok := p[providerName]
	return provider.Price{In: e.in, Out: e.out}, ok
}
func (p safetyPrice) Cheapest(providerName string) (provider.Price, bool) {
	e, ok := p[providerName]
	return provider.Price{In: e.in, Out: e.out}, ok
}
func (p safetyPrice) CheapestModel(providerName string) (string, provider.Price, bool) {
	e, ok := p[providerName]
	return e.model, provider.Price{In: e.in, Out: e.out}, ok
}

// safetyCap 实现 CapabilityResolver + BestModelResolver：dead 家有
// 能力分（会进 @quality 候选），kilo 家无（正是真实世界的形态）。
type safetyCap struct{}

func (safetyCap) Capability(providerName, model string) (float64, bool) {
	if providerName == "dead" {
		return 90, true
	}
	return 0, false
}
func (safetyCap) BestModel(providerName string) (string, float64, bool) {
	if providerName == "dead" {
		return "dead-best", 90, true
	}
	return "", 0, false
}

// deadUpstream 模拟密钥失效的 keyed 提供商：一律 401。
func deadUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
	}))
}

// TestQualityAutoFallsBackToFreePool 端到端：能力候选（dead）全 401 →
// 免费安全网（kilo-auto/free）接住——管家场景的直接回归门。
func TestQualityAutoFallsBackToFreePool(t *testing.T) {
	dead := deadUpstream(t)
	defer dead.Close()
	free := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, okCompletion("kilo-auto/free"))
	}))
	defer free.Close()

	r := &Router{
		Providers: []provider.Provider{
			newMockAdapter(t, "dead", dead.URL),
			newMockAdapter(t, "kilo", free.URL),
		},
		Capability: safetyCap{},
		Price: safetyPrice{
			"dead": {model: "dead-best", in: 0, out: 0},   // dead 的"最强模型"也免费声明（不影响：先试先败）
			"kilo": {model: "kilo-auto/free", in: 0, out: 0},
		},
	}
	req := &schema.UnifiedRequest{Model: "", Messages: []schema.Message{
		{Role: "user", Content: schema.NewTextContent("hi")},
	}}
	resp, attempts, err := r.Dispatch(context.Background(), req, WithQualityAuto())
	if err != nil {
		t.Fatalf("Dispatch should fall back to the free pool, got: %v", err)
	}
	if resp.ProviderName != "kilo" {
		t.Fatalf("winner = %s, want kilo (free safety net)", resp.ProviderName)
	}
	if len(attempts) < 2 || attempts[0].Provider != "dead" {
		t.Fatalf("attempts = %+v, want dead tried first then kilo", attempts)
	}
}

// TestPremiumAutoFallsBackToFreePool 付费家全灭（401）→ 免费网兜底。
func TestPremiumAutoFallsBackToFreePool(t *testing.T) {
	dead := deadUpstream(t)
	defer dead.Close()
	free := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, okCompletion("kilo-auto/free"))
	}))
	defer free.Close()

	r := &Router{
		Providers: []provider.Provider{
			newMockAdapter(t, "paid", dead.URL),
			newMockAdapter(t, "kilo", free.URL),
		},
		Capability: safetyCap{},
		Price: safetyPrice{
			"paid": {model: "paid-best", in: 3, out: 15},
			"kilo": {model: "kilo-auto/free", in: 0, out: 0},
		},
	}
	req := &schema.UnifiedRequest{Model: "", Messages: []schema.Message{
		{Role: "user", Content: schema.NewTextContent("hi")},
	}}
	resp, _, err := r.Dispatch(context.Background(), req, WithStrategyName("premium"))
	if err != nil {
		t.Fatalf("Dispatch should fall back to the free pool, got: %v", err)
	}
	if resp.ProviderName != "kilo" {
		t.Fatalf("winner = %s, want kilo", resp.ProviderName)
	}
}

// TestFreeSafetyNetDedup 既有候选不重复（@cheap 语义自含免费池，安全
// 网不重复追加同 (provider, model)）。
func TestFreeSafetyNetDedup(t *testing.T) {
	free := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, okCompletion("kilo-auto/free"))
	}))
	defer free.Close()
	r := &Router{
		Providers: []provider.Provider{newMockAdapter(t, "kilo", free.URL)},
		Price:     safetyPrice{"kilo": {model: "kilo-auto/free", in: 0, out: 0}},
	}
	existing := []candidate{{p: r.Providers[0], model: "kilo-auto/free"}}
	if got := r.freeSafetyNet(existing); len(got) != 0 {
		t.Fatalf("dedup failed: %+v", got)
	}
	fresh := r.freeSafetyNet(nil)
	if len(fresh) != 1 || fresh[0].model != "kilo-auto/free" {
		t.Fatalf("fresh net = %+v", fresh)
	}
	// 有偿家不入网。
	r2 := &Router{
		Providers: []provider.Provider{newMockAdapter(t, "paid", free.URL)},
		Price:     safetyPrice{"paid": {model: "m", in: 1, out: 1}},
	}
	if got := r2.freeSafetyNet(nil); len(got) != 0 {
		t.Fatalf("paid provider must not enter the net: %+v", got)
	}
}
