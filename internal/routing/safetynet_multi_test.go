// safetynet_multi_test.go 覆盖免费安全网的多模型冗余（v0.1.35）：
// 同一家多个登记免费模型入网（帽 3），聚合入口瞬时抖动时第二个
// 免费模型接住；FreeModels 只收 0/0 且在列的模型。
package routing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
)

// multiFreePrice 实现 PriceResolver + freeModelLister（Catalog 之外的
// 测试替身：kilo 三免费模型）。
type multiFreePrice struct{}

func (multiFreePrice) Price(providerName, model string) (provider.Price, bool) {
	if providerName == "kilo" {
		return provider.Price{}, true
	}
	return provider.Price{}, false
}
func (multiFreePrice) Cheapest(providerName string) (provider.Price, bool) {
	if providerName == "kilo" {
		return provider.Price{}, true
	}
	return provider.Price{}, false
}
func (multiFreePrice) CheapestModel(providerName string) (string, provider.Price, bool) {
	if providerName == "kilo" {
		return "kilo-auto/free", provider.Price{}, true
	}
	return "", provider.Price{}, false
}
func (multiFreePrice) FreeModels(providerName string) []string {
	if providerName == "kilo" {
		return []string{"kilo-auto/free", "kilo/alpha:free", "kilo/beta:free"}
	}
	return nil
}

// TestFreeNetSurvivesEntryModelBlip 聚合入口（kilo-auto/free）500 抖动
// → 网内第二个免费模型接住——单模型网的瞬时击穿场景。
func TestFreeNetSurvivesEntryModelBlip(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch body.Model {
		case "kilo-auto/free":
			w.WriteHeader(http.StatusInternalServerError) // 聚合入口抖动
			io.WriteString(w, `{"error":{"message":"transient blip"}}`)
		default:
			io.WriteString(w, okCompletion(body.Model))
		}
	}))
	t.Cleanup(up.Close)

	r := &Router{
		Providers:  []provider.Provider{newMockAdapter(t, "kilo", up.URL)},
		Capability: budgetCap{}, // L 空 → 纯净网路径
		Price:      multiFreePrice{},
	}
	req := &schema.UnifiedRequest{Model: "", Messages: []schema.Message{
		{Role: "user", Content: schema.NewTextContent("hi")},
	}}
	resp, attempts, err := r.Dispatch(context.Background(), req, WithQualityAuto())
	if err != nil {
		t.Fatalf("Dispatch: %v (attempts %+v)", err, attempts)
	}
	if resp.ProviderName != "kilo" || resp.Model == "kilo-auto/free" {
		t.Fatalf("winner = %s/%s, want kilo 的非入口免费模型", resp.ProviderName, resp.Model)
	}
	if len(attempts) < 2 {
		t.Fatalf("attempts = %d, want 入口失败+第二模型成功", len(attempts))
	}
}

// TestFreeModelsDeclaredOnly FreeModels 数据面：只收 0/0 且在列。
func TestFreeModelsDeclaredOnly(t *testing.T) {
	// 用真 Catalog：静态声明 1 免费 + 1 付费 → 只出免费。
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"free-a"},{"id":"paid-x"}]}`)
	}))
	t.Cleanup(up.Close)
	adapter, err := openai_compat.New(openai_compat.Spec{
		ProviderName: "fm", BaseURL: up.URL + "/v1", APIKey: "k",
	})
	if err != nil {
		t.Fatal(err)
	}
	static := map[string][]provider.ModelInfo{"fm": {{ID: "free-a"}, {ID: "paid-x"}}}
	staticPrices := map[string]map[string]provider.Price{"fm": {
		"free-a": {In: 0, Out: 0}, "paid-x": {In: 1, Out: 2},
	}}
	cat := NewCatalog([]provider.Provider{adapter}, static, nil, nil, nil)
	cat.Sync(context.Background())
	cat.SetStaticPrices(staticPrices)
	got := cat.FreeModels("fm")
	if len(got) != 1 || got[0] != "free-a" {
		t.Fatalf("FreeModels = %v, want [free-a]", got)
	}
}
