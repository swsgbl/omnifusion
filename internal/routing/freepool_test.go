// freepool_test.go 覆盖免费安全网的数据面（2026-11-25 用户实撞）：
// 静态清单不与 live 取并集（Kilo 静态清单里转收费的旧模型每个请求都
// 进安全网，401 之后整家被隔离）；上游自带的免费标记（isFree / 零定价）
// 让免费池轮换后安全网仍然有模型可试。
package routing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
)

func freePoolCatalog(t *testing.T, baseURL string) *Catalog {
	t.Helper()
	adapter, err := openai_compat.New(openai_compat.Spec{ProviderName: "pool", BaseURL: baseURL, APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	static := map[string][]provider.ModelInfo{"pool": {
		{ID: "curated-a"}, {ID: "curated-b"}, {ID: "stale-gone"}, // 静态 0/0，stale-gone 早已下线
	}}
	prices := map[string]map[string]provider.Price{"pool": {
		"curated-a": {In: 0, Out: 0}, "curated-b": {In: 0, Out: 0}, "stale-gone": {In: 0, Out: 0},
	}}
	cat := NewCatalog([]provider.Provider{adapter}, static, nil, nil, nil)
	cat.SetStaticPrices(prices)
	cat.Sync(context.Background())
	return cat
}

// TestFreeModelsServeableOnlyAndUpstreamFlagged 有 live 快照时只在 live
// 里挑：静态里早已下线的 stale-gone 出局；上游标记的免费模型（isFree
// 与零定价）入网，排在登记免费价之后。
func TestFreeModelsServeableOnlyAndUpstreamFlagged(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"data":[{"id":"curated-a"},{"id":"curated-b"},{"id":"flagged-f","isFree":true},`+
			`{"id":"zero-z","pricing":{"prompt":"0","completion":"0"}},{"id":"paid-p"},{"id":"half-h","pricing":{"prompt":"0"}}]}`)
	}))
	defer up.Close()
	cat := freePoolCatalog(t, up.URL+"/v1")

	got := cat.FreeModels("pool")
	want := []string{"curated-a", "curated-b", "flagged-f", "zero-z"}
	if len(got) != len(want) {
		t.Fatalf("FreeModels = %v, want %v (stale-gone out, half-declared not free)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("FreeModels = %v, want %v", got, want)
		}
	}
	if id, _, ok := cat.CheapestModel("pool"); !ok || id == "stale-gone" {
		t.Fatalf("CheapestModel = (%q, %v): must not recommend a retired static entry", id, ok)
	}
}

// TestFreeModelsStaticFallbackWithoutLive 无 live 快照（上游不可达的
// 冷启动）：回落静态声明，静态条目此时可入网。
func TestFreeModelsStaticFallbackWithoutLive(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	dead.Close() // 立刻关掉：连接被拒 → 冷启动回落静态
	cat := freePoolCatalog(t, dead.URL+"/v1")

	got := cat.FreeModels("pool")
	want := []string{"curated-a", "curated-b", "stale-gone"}
	if len(got) != len(want) {
		t.Fatalf("FreeModels (static fallback) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("FreeModels (static fallback) = %v, want %v", got, want)
		}
	}
}

// TestFreeSafetyNetSkipsLockedModels 正被模型级隔离的免费模型不占名额
//（帽 3）：一个池里前三个恰好都被锁时，后面的可用模型仍能入网。
func TestFreeSafetyNetSkipsLockedModels(t *testing.T) {
	free := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, okCompletion("c-ok"))
	}))
	defer free.Close()
	iso, err := NewIsolation(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &Router{
		Providers: []provider.Provider{newMockAdapter(t, "kilo", free.URL)},
		Isolation: iso,
		Price:     poolFix{free: map[string][]string{"kilo": {"a-lock", "b-lock", "c-ok", "d-ok", "e-ok"}}},
	}
	iso.LockModel("kilo", "a-lock", time.Minute, KindModelGone)
	iso.LockModel("kilo", "b-lock", time.Minute, KindQuotaExhausted)

	got := r.freeSafetyNet(nil)
	var models []string
	for _, c := range got {
		models = append(models, c.model)
	}
	want := []string{"c-ok", "d-ok", "e-ok"}
	if len(models) != len(want) {
		t.Fatalf("net = %v, want %v (locked models must not consume the cap)", models, want)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Fatalf("net = %v, want %v", models, want)
		}
	}
}
