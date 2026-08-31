package routing

import (
	"testing"

	"github.com/swsgbl/omnifusion/internal/catalogfeed"
	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
)

// fakePrices 按固定表应答定价查询（cheap v2 测试注入）。
type fakePrices struct {
	byPM map[string]provider.Price // "provider\x00model" → 价格
	byP  map[string]provider.Price // provider → 最低价（无目标模型）
}

func (f fakePrices) Price(p, m string) (provider.Price, bool) {
	v, ok := f.byPM[p+"\x00"+m]
	return v, ok
}

func (f fakePrices) Cheapest(p string) (provider.Price, bool) {
	v, ok := f.byP[p]
	return v, ok
}

func (f fakePrices) CheapestModel(p string) (string, provider.Price, bool) {
	if _, ok := f.byP[p]; ok {
		return "cheapest-of-" + p, f.byP[p], true
	}
	return "", provider.Price{}, false
}

// TestCheapTrueCostTiers 三档语义：登记免费 → 未登记 → 已定价（成本
// 升序）；无定价源的 v1 余量语义由既有 TestBuiltinStrategyOrders 覆盖。
func TestCheapTrueCostTiers(t *testing.T) {
	r := newScoringRouter(t, "free", "unknown", "dear", "cheapaid")
	r.Price = fakePrices{byPM: map[string]provider.Price{
		"free\x00m":    {In: 0, Out: 0},
		"dear\x00m":    {In: 3, Out: 6},
		"cheapaid\x00m": {In: 1, Out: 2},
	}}
	got := r.ordered("cheap", "m", 1000)
	want := []string{"free", "unknown", "cheapaid", "dear"}
	for i, w := range want {
		if got[i].Name() != w {
			var names []string
			for _, p := range got {
				names = append(names, p.Name())
			}
			t.Fatalf("cheap tiers = %v, want %s at #%d", names, w, i)
		}
	}
}

// TestCheapCostScalesWithPromptTokens 成本随压缩后 token 变化：短请求
// 选输出便宜的，长请求翻向输入便宜的（真成本排序的核心行为）。
func TestCheapCostScalesWithPromptTokens(t *testing.T) {
	r := newScoringRouter(t, "outcheap", "incheap")
	r.Price = fakePrices{byPM: map[string]provider.Price{
		"outcheap\x00m": {In: 1, Out: 1},
		"incheap\x00m":  {In: 0.2, Out: 5},
	}}
	if got := r.ordered("cheap", "m", 0); got[0].Name() != "outcheap" {
		t.Fatalf("short-request cheap rank starts with %s, want outcheap", got[0].Name())
	}
	if got := r.ordered("cheap", "m", 100000); got[0].Name() != "incheap" {
		t.Fatalf("long-request cheap rank starts with %s, want incheap", got[0].Name())
	}
}

// TestCheapFreeTierHeadroomTiebreak 免费档内保持 v1 语义：先花最富裕的
// 免费额度。
func TestCheapFreeTierHeadroomTiebreak(t *testing.T) {
	r := newScoringRouter(t, "half", "full")
	qt := NewQuotaTracker()
	qt.SetLimit("half", QuotaLimits{RPM: 2})
	qt.RecordRequest("half")
	r.Quota = qt
	r.Price = fakePrices{byPM: map[string]provider.Price{
		"half\x00m": {In: 0, Out: 0},
		"full\x00m": {In: 0, Out: 0},
	}}
	if got := r.ordered("cheap", "m", 0); got[0].Name() != "full" {
		t.Fatalf("free-tier cheap rank starts with %s, want full (most headroom)", got[0].Name())
	}
}

// TestCheapAutoPicksPerProviderCheapest 裸 @cheap 候选生成：每家带自家
// 登记最低价模型 id（不再是空模型名），免费档在前、同档余量降序。
func TestCheapAutoPicksPerProviderCheapest(t *testing.T) {
	r := newScoringRouter(t, "free-a", "paid-b", "nokey-c")
	r.Price = fakePrices{
		byPM: map[string]provider.Price{},
		byP: map[string]provider.Price{
			"free-a":  {In: 0, Out: 0},
			"paid-b":  {In: 1, Out: 2},
			"nokey-c": {In: 5, Out: 5},
		},
	}
	cands, err := r.candidatesFor(dispatchConfig{strategyName: "cheap"}, &schema.UnifiedRequest{})
	if err != nil {
		t.Fatalf("candidatesFor: %v", err)
	}
	if len(cands) != 3 {
		t.Fatalf("candidates = %d, want 3", len(cands))
	}
	if cands[0].p.Name() != "free-a" || cands[0].model != "cheapest-of-free-a" {
		t.Fatalf("first = %s/%s, want free-a/cheapest-of-free-a", cands[0].p.Name(), cands[0].model)
	}
	if cands[1].p.Name() != "paid-b" { // 付费按价升序在前
		t.Fatalf("second = %s, want paid-b", cands[1].p.Name())
	}
	for _, c := range cands {
		if c.model == "" {
			t.Fatalf("candidate %s has empty model (would send blank upstream)", c.p.Name())
		}
	}
}

// TestCatalogPriceAndCheapest 验证 Catalog 定价查询：feed 价优先于注册表
// 静态价、精确/后缀匹配口径同 Capability、Cheapest 合并取最低。
func TestCatalogPriceAndCheapest(t *testing.T) {
	c := NewCatalog(nil, nil, nil, nil, nil)
	c.SetStaticPrices(map[string]map[string]provider.Price{
		"demo": {
			"demo/mini": {In: 0, Out: 0},
			"demo/big":  {In: 5, Out: 15},
		},
	})
	if p, ok := c.Price("demo", "mini"); !ok || p.In != 0 || p.Out != 0 {
		t.Fatalf("static price demo/mini = %+v ok=%v, want explicit free", p, ok)
	}
	if p, ok := c.Price("demo", "big"); !ok || p.In != 5 || p.Out != 15 {
		t.Fatalf("static price demo/big = %+v ok=%v, want 5/15", p, ok)
	}
	if _, ok := c.Price("demo", "missing"); ok {
		t.Error("unknown model should be unpriced")
	}

	// feed 价优先（社区可随版本调价）；裸名按 "厂商/模型" 后缀匹配。
	in, out := 2.0, 6.0
	c.ApplyFeed(&catalogfeed.Feed{
		Version:     2,
		GeneratedAt: 1,
		Providers: map[string]catalogfeed.ProviderFeed{
			"demo": {Models: []catalogfeed.ModelEntry{{
				ID: "demo/big", Status: catalogfeed.StatusStable,
				PriceIn: &in, PriceOut: &out,
			}}},
		},
	})
	if p, ok := c.Price("demo", "demo/big"); !ok || p.In != 2 || p.Out != 6 {
		t.Fatalf("feed price should override static (exact id): %+v ok=%v", p, ok)
	}
	if p, ok := c.Price("demo", "big"); !ok || p.In != 2 || p.Out != 6 {
		t.Fatalf("suffix match for bare 'big' = %+v ok=%v, want feed 2/6", p, ok)
	}
	if _, ok := c.Cheapest("nobody"); ok {
		t.Error("unknown provider should have no cheapest price")
	}
}
