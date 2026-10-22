// premium_test.go 覆盖付费优先策略：付费档在前（能力分降序）、
// 免费池殿后；预算耗尽整序反转；无定价源保持注册序。
package routing

import (
	"context"
	"net/http"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
)

// premPrice 是 premium 测试的定价源。
type premPrice map[string]provider.Price

func (p premPrice) Price(providerName, model string) (provider.Price, bool) {
	v, ok := p[providerName+"/"+model]
	if !ok {
		v, ok = p[providerName]
	}
	return v, ok
}
func (p premPrice) Cheapest(providerName string) (provider.Price, bool) {
	v, ok := p[providerName]
	return v, ok
}
func (p premPrice) CheapestModel(providerName string) (string, provider.Price, bool) {
	v, ok := p[providerName]
	return "m", v, ok
}

// premCap 是能力分源。
type premCap map[[2]string]float64

func (c premCap) Capability(providerName, model string) (float64, bool) {
	v, ok := c[[2]string{providerName, model}]
	return v, ok
}

// namedStub 是最小 Provider 桩（premium 只读 Name）。
type namedStub struct{ name string }

func (n *namedStub) Name() string { return n.name }
func (n *namedStub) Capabilities() provider.Capability {
	return provider.Capability{InputModalities: []string{"text"}}
}
func (n *namedStub) HTTPClient() *http.Client { return nil }
func (n *namedStub) Translate(context.Context, *schema.UnifiedRequest) (*provider.ProviderCall, error) {
	return nil, nil
}
func (n *namedStub) Parse(context.Context, *provider.ProviderCall, *http.Response) (*schema.Response, error) {
	return nil, nil
}
func (n *namedStub) ListModels(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func mockProv(name string) provider.Provider { return &namedStub{name: name} }

func premNames(ps []provider.Provider) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name()
	}
	return out
}

func TestPremiumPaidFirst(t *testing.T) {
	pr := premPrice{
		"paid-a": {In: 3, Out: 15},
		"paid-b": {In: 1, Out: 2},
		"free-c": {In: 0, Out: 0},
	}
	cap := premCap{
		{"paid-a", "m"}: 70,
		{"paid-b", "m"}: 90,
		{"free-c", "m"}: 99,
	}
	st := premiumStrategy{pr: pr, cap: cap}
	cands := []provider.Provider{mockProv("free-c"), mockProv("paid-a"), mockProv("paid-b")}
	got := st.Order(cands, &RouteContext{Model: "m"})
	// 付费档在前（档内能力分降序：b>a），免费殿后——尽管免费能力分最高。
	want := []string{"paid-b", "paid-a", "free-c"}
	for i, w := range want {
		if got[i].Name() != w {
			t.Fatalf("order[%d] = %s, want %s (full: %v)", i, got[i].Name(), w, premNames(got))
		}
	}
}

func TestPremiumBudgetExhaustedInverts(t *testing.T) {
	pr := premPrice{
		"paid-a": {In: 3, Out: 15},
		"free-c": {In: 0, Out: 0},
	}
	st := premiumStrategy{pr: pr, spend: func() float64 { return 2.5 }, budget: 2.0}
	got := st.Order([]provider.Provider{mockProv("paid-a"), mockProv("free-c")}, &RouteContext{Model: "m"})
	if got[0].Name() != "free-c" || got[1].Name() != "paid-a" {
		t.Fatalf("budget-exhausted order = %v, want free first", premNames(got))
	}
}

func TestPremiumNoPriceSourceKeepsOrder(t *testing.T) {
	st := premiumStrategy{}
	got := st.Order([]provider.Provider{mockProv("a"), mockProv("b")}, nil)
	if got[0].Name() != "a" {
		t.Fatal("no price source must keep registration order")
	}
}

// TestPremiumDirectiveKnown 确认 @premium 进入指令面（ParseModelDirective）。
func TestPremiumDirectiveKnown(t *testing.T) {
	strategy, bare, err := ParseModelDirective("@premium:gpt-x")
	if err != nil || strategy != "premium" || bare != "gpt-x" {
		t.Fatalf("directive = (%q,%q,%v), want (premium,gpt-x)", strategy, bare, err)
	}
}
