package routing

import (
	"testing"

	"github.com/swsgbl/omnifusion/internal/catalogfeed"
	"github.com/swsgbl/omnifusion/internal/provider"
)

// providerNames 是候选名列表（断言输出用）。
func providerNames(ps []provider.Provider) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name()
	}
	return out
}

// fakeCaps 是测试用能力分查询面。
type fakeCaps struct{ m map[string]float64 }

func (f fakeCaps) Capability(provider, model string) (float64, bool) {
	v, ok := f.m[provider+"/"+model]
	return v, ok
}

func TestQualityStrategyOrdersByCapability(t *testing.T) {
	r := newScoringRouter(t, "a", "b", "c")
	r.Capability = fakeCaps{m: map[string]float64{
		"a/m": 80, "b/m": 95, // c 未评级
	}}
	got := r.ordered("quality", "m", 0)
	want := []string{"b", "a", "c"} // b(95) > a(80) > c(0，注册序殿后)
	for i, n := range want {
		if got[i].Name() != n {
			t.Fatalf("quality order = %v, want %v", providerNames(got), want)
		}
	}
}

func TestQualityStrategyNoDataKeepsRegistryOrder(t *testing.T) {
	r := newScoringRouter(t, "a", "b")
	r.Capability = fakeCaps{m: map[string]float64{"b/m": 0}} // 全 0 分
	got := r.ordered("quality", "m", 0)
	if got[0].Name() != "a" || got[1].Name() != "b" {
		t.Fatalf("quality no-data order = %v, want registry order", providerNames(got))
	}

	r2 := newScoringRouter(t, "a", "b") // 无能力源：退化注册序
	got2 := r2.ordered("quality", "m", 0)
	if got2[0].Name() != "a" {
		t.Fatalf("quality nil-resolver order = %v", providerNames(got2))
	}

	got3 := r.ordered("quality", "", 0) // 无目标模型：保持注册序
	if got3[0].Name() != "a" {
		t.Fatalf("quality no-model order = %v", providerNames(got3))
	}
}

func TestQualityDirectiveParses(t *testing.T) {
	name, bare, err := ParseModelDirective("@quality:llama-3.3-70b")
	if err != nil || name != "quality" || bare != "llama-3.3-70b" {
		t.Fatalf("parse = %q/%q/%v", name, bare, err)
	}
	name, bare, err = ParseModelDirective("@quality") // 裸形：自动选最强
	if err != nil || name != "quality" || bare != "" {
		t.Fatalf("bare parse = %q/%q/%v", name, bare, err)
	}
}

// fakeBest 是同时实现 Capability 与 BestModel 的测试目录替身。
type fakeBest struct {
	fakeCaps
	best map[string]fakeBestEntry
}
type fakeBestEntry struct {
	id  string
	cap float64
}

func (f fakeBest) BestModel(p string) (string, float64, bool) {
	e, ok := f.best[p]
	return e.id, e.cap, ok
}

func TestQualityAutoPicksEachProvidersBest(t *testing.T) {
	r := newScoringRouter(t, "weak", "strong")
	r.Capability = fakeBest{
		fakeCaps: fakeCaps{m: map[string]float64{}},
		best: map[string]fakeBestEntry{
			"weak":   {id: "weak-small", cap: 40},
			"strong": {id: "strong-big", cap: 95},
		},
	}
	cands := r.candidatesFor(dispatchConfig{qualityAuto: true}, nil)
	if len(cands) != 2 {
		t.Fatalf("candidates = %d, want 2", len(cands))
	}
	if cands[0].p.Name() != "strong" || cands[0].model != "strong-big" {
		t.Fatalf("first = %s/%s, want strong/strong-big", cands[0].p.Name(), cands[0].model)
	}
	if cands[1].p.Name() != "weak" || cands[1].model != "weak-small" {
		t.Fatalf("second = %s/%s, want weak/weak-small", cands[1].p.Name(), cands[1].model)
	}
}

func TestQualityAutoNoBestDataYieldsNone(t *testing.T) { // 无 feed：零候选（边界 400 已拦）
	r := newScoringRouter(t, "a", "b")
	r.Capability = fakeBest{best: map[string]fakeBestEntry{}} // 空
	if cands := r.candidatesFor(dispatchConfig{qualityAuto: true}, nil); len(cands) != 0 {
		t.Fatalf("candidates = %d, want 0", len(cands))
	}
}

func TestCatalogCapabilityFromFeed(t *testing.T) {
	c := NewCatalog(nil, nil, nil, nil, nil)
	n := c.ApplyFeed(&catalogfeed.Feed{
		Version: 1, GeneratedAt: 1,
		Providers: map[string]catalogfeed.ProviderFeed{
			"groq": {Models: []catalogfeed.ModelEntry{
				{ID: "llama-3.3-70b", Status: "stable", Capability: 88},
				{ID: "openai/gpt-oss-120b", Status: "stable", Capability: 92},
			}},
		},
	})
	if n != 2 {
		t.Fatalf("applied = %d", n)
	}
	if v, ok := c.Capability("groq", "llama-3.3-70b"); !ok || v != 88 {
		t.Fatalf("exact lookup = %v/%v", v, ok)
	}
	if v, ok := c.Capability("groq", "gpt-oss-120b"); !ok || v != 92 { // 后缀匹配
		t.Fatalf("suffix lookup = %v/%v", v, ok)
	}
	if _, ok := c.Capability("groq", "unknown"); ok {
		t.Fatal("unknown model must not resolve")
	}
	if _, ok := c.Capability("other", "llama-3.3-70b"); ok {
		t.Fatal("unknown provider must not resolve")
	}
}
