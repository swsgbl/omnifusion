package openai_compat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
)

func TestListModelsLive(t *testing.T) {
	var sawAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		// OpenRouter 形 context_length 与 Groq 形 context_window 并存
		io.WriteString(w, `{"data":[{"id":"a","context_length":4096},`+
			`{"id":"b","context_window":8192},{"id":""}]}`)
	}))
	defer up.Close()

	a, err := New(Spec{ProviderName: "x", BaseURL: up.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if sawAuth != "Bearer k" {
		t.Errorf("auth = %q, want Bearer k", sawAuth)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v, want 2 (blank id dropped)", models)
	}
	if models[0].ID != "a" || models[0].ContextWindow != 4096 {
		t.Errorf("models[0] = %+v", models[0])
	}
	if models[1].ID != "b" || models[1].ContextWindow != 8192 {
		t.Errorf("models[1] = %+v (context_window fallback)", models[1])
	}
}

func TestListModelsNon2xx(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"boom"}`)
	}))
	defer up.Close()

	a, err := New(Spec{ProviderName: "x", BaseURL: up.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = a.ListModels(context.Background())
	ue, ok := provider.IsUpstream(err)
	if !ok {
		t.Fatalf("err = %v, want UpstreamError", err)
	}
	if ue.Status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", ue.Status)
	}
}

func TestListModelsEmptyData(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[]}`)
	}))
	defer up.Close()

	a, err := New(Spec{ProviderName: "x", BaseURL: up.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := a.ListModels(context.Background()); err == nil {
		t.Fatal("empty catalog must be an error, not a silent wipe")
	}
}

// TestListModelsFreeOnlyFilter 免密网关把免费池与付费 id 混在同一份
// 清单：FreeOnly 只保留 isFree:true；未标记（nil）与 true 都保留，
// 显式 false 排除——不误伤没有该字段的常规清单（filter 关闭时全收）。
func TestListModelsFreeOnlyFilter(t *testing.T) {
	listing := `{"data":[{"id":"kilo-auto/free","isFree":true},` +
		`{"id":"paid/claude","isFree":false},{"id":"unmarked"}]}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, listing)
	}))
	defer up.Close()

	freeOnly, err := New(Spec{ProviderName: "kilo", BaseURL: up.URL, FreeOnly: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	models, err := freeOnly.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 || models[0].ID != "kilo-auto/free" || models[1].ID != "unmarked" {
		t.Fatalf("free-only models = %+v, want [kilo-auto/free unmarked]", models)
	}

	all, err := New(Spec{ProviderName: "x", BaseURL: up.URL, FreeOnly: false})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	models, err = all.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("unfiltered models = %+v, want all 3", models)
	}
}

// TestListModelsLenientShapes 清单字段漂移不许打掉整份目录（2026-11-25
// 用户实撞：LLM7 把 context_window 从数字改成 {"tokens":…,"chars":…}，
// 解码报错后该家目录多周停在旧快照）：对象形态取 tokens，数组/怪形状
// 解码成 0（窗口未知 → 路由不过滤），整份清单照常返回。
func TestListModelsLenientShapes(t *testing.T) {
	listing := `{"data":[` +
		`{"id":"obj","context_window":{"tokens":512000,"chars":null}},` +
		`{"id":"arr","context_window":[1,2,3]},` +
		`{"id":"str","context_window":"8192"},` +
		`{"id":"null","context_window":null},` +
		`{"id":"weird","context_window":{"foo":[1,2]}},` +
		`{"id":"both","context_length":4096,"context_window":{"tokens":999}}]}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, listing)
	}))
	defer up.Close()

	a, err := New(Spec{ProviderName: "x", BaseURL: up.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v (field drift must not blank the catalog)", err)
	}
	want := map[string]int64{
		"obj": 512000, "arr": 0, "str": 8192, "null": 0, "weird": 0, "both": 4096,
	}
	if len(models) != len(want) {
		t.Fatalf("models = %+v, want %d entries", models, len(want))
	}
	for _, m := range models {
		if m.ContextWindow != want[m.ID] {
			t.Errorf("%s ctx = %d, want %d", m.ID, m.ContextWindow, want[m.ID])
		}
	}
}

// TestListModelsFreeEvidence 免费的两种上游证据：isFree:true 与 OpenRouter
// 式 prompt/completion 双零定价（字符串数字）。缺字段、单边为 0、定价块
// 缺失、非数字都不算免费——"未声明"永不等于"免费"。
func TestListModelsFreeEvidence(t *testing.T) {
	listing := `{"data":[` +
		`{"id":"flagged","isFree":true},` +
		`{"id":"flagged-false","isFree":false},` +
		`{"id":"zero-priced","pricing":{"prompt":"0","completion":"0"}},` +
		`{"id":"paid","pricing":{"prompt":"0.3","completion":"0.5"}},` +
		`{"id":"half-zero","pricing":{"prompt":"0"}},` +
		`{"id":"no-pricing"},` +
		`{"id":"junk-pricing","pricing":{"prompt":"n/a","completion":"0"}}]}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, listing)
	}))
	defer up.Close()

	a, err := New(Spec{ProviderName: "openrouter", BaseURL: up.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	free := map[string]bool{}
	for _, m := range models {
		free[m.ID] = m.Free
	}
	for _, id := range []string{"flagged", "zero-priced"} {
		if !free[id] {
			t.Errorf("%s: Free = false, want true", id)
		}
	}
	for _, id := range []string{"flagged-false", "paid", "half-zero", "no-pricing", "junk-pricing"} {
		if free[id] {
			t.Errorf("%s: Free = true, want false", id)
		}
	}
}

// TestKeylessAndAggregatorMarks 可选接口的取值来源：Keyless 只看有没有
// key；IsAggregator 来自 Spec（注册表 aggregator: true）。
func TestKeylessAndAggregatorMarks(t *testing.T) {
	keyless, err := New(Spec{ProviderName: "kilo", BaseURL: "http://kilo.test/v1", Aggregator: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	keyed, err := New(Spec{ProviderName: "x", BaseURL: "http://x.test/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !keyless.Keyless() || keyed.Keyless() {
		t.Errorf("Keyless: keyless=%v keyed=%v, want true/false", keyless.Keyless(), keyed.Keyless())
	}
	if !keyless.IsAggregator() || keyed.IsAggregator() {
		t.Errorf("IsAggregator: flag=%v default=%v, want true/false", keyless.IsAggregator(), keyed.IsAggregator())
	}
	var p provider.Provider = keyless
	if _, ok := p.(provider.Anonymous); !ok {
		t.Error("Adapter does not implement provider.Anonymous")
	}
	if _, ok := p.(provider.Aggregator); !ok {
		t.Error("Adapter does not implement provider.Aggregator")
	}
}
