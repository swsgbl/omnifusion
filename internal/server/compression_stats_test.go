// compression_stats_test.go 覆盖 M5.6 压缩统计面：combo 请求后
// /dashboard/api/compression/stats 聚合正确（组合与阶段两级、saved 为
// 正）、scope 鉴权（route-scoped 403 / compression-scoped 200）、
// 未装配组合时空态。
package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/compression"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/store"
)

// newStatsFixture 起一个装配了 mock 上游与 dedup+caveman 组合管线的
// 被测网关。
func newStatsFixture(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"m",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
	}))
	t.Cleanup(up.Close)
	adapter, err := openai_compat.New(openai_compat.Spec{
		ProviderName: "mock", BaseURL: up.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	pipe, err := compression.BuildCombo([]string{"dedup", "caveman"})
	if err != nil {
		t.Fatalf("BuildCombo: %v", err)
	}
	router := &routing.Router{Providers: []provider.Provider{adapter}}
	router.Combos = map[string]routing.Combo{"shrink": {
		Name: "shrink", Members: []routing.ComboMember{{Provider: "mock", Model: "m"}},
		Compression: "shrink"}}
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), st))
	s.SetRouter(router)
	s.SetComboPipelines(map[string]*compression.Pipeline{"shrink": pipe})
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	return gw
}

// postCombo 发一个走命名组合的压缩请求（5 条重复冗长消息触发 dedup）。
func postCombo(t *testing.T, gw *httptest.Server) {
	t.Helper()
	verbose := strings.Repeat("In order to please basically review this carefully. ", 4)
	var msgs []string
	for i := 0; i < 5; i++ {
		msgs = append(msgs, `{"role":"user","content":"`+verbose+`"}`)
	}
	body := `{"model":"@combo:shrink","messages":[` + strings.Join(msgs, ",") + `]}`
	resp := postAuthed(t, gw.URL+"/v1/chat/completions", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("combo request = %d, want 200", resp.StatusCode)
	}
}

// statsResp 取压缩统计 API（token 可换）。
func statsResp(t *testing.T, gw *httptest.Server, token string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, gw.URL+"/dashboard/api/compression/stats", nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.StatusCode, body
}

func TestCompressionStatsAggregates(t *testing.T) {
	gw := newStatsFixture(t)
	postCombo(t, gw)

	status, body := statsResp(t, gw, testGatewayToken)
	if status != http.StatusOK {
		t.Fatalf("stats = %d, want 200", status)
	}
	combos, _ := body["combos"].([]any)
	if len(combos) != 1 {
		t.Fatalf("combos = %v, want 1 row", combos)
	}
	c := combos[0].(map[string]any)
	if c["combo"] != "shrink" {
		t.Errorf("combo name = %v", c["combo"])
	}
	cs := c["stats"].(map[string]any)
	if cs["runs"].(float64) != 1 {
		t.Errorf("combo runs = %v, want 1", cs["runs"])
	}
	if cs["tokens_after"].(float64) >= cs["tokens_before"].(float64) {
		t.Errorf("tokens %v → %v, want shrink", cs["tokens_before"], cs["tokens_after"])
	}

	stages, _ := body["stages"].([]any)
	if len(stages) != 2 {
		t.Fatalf("stages = %v, want session_dedup+caveman", stages)
	}
	names := map[string]bool{}
	for _, raw := range stages {
		srow := raw.(map[string]any)
		names[srow["stage"].(string)] = true
		ss := srow["stats"].(map[string]any)
		if ss["runs"].(float64) != 1 || ss["applied"].(float64) != 1 {
			t.Errorf("stage %v stats = %v, want runs=1 applied=1", srow["stage"], ss)
		}
	}
	if !names["session_dedup"] || !names["caveman"] {
		t.Errorf("stage names = %v, want session_dedup+caveman", names)
	}
}

func TestCompressionStatsScopeAndEmpty(t *testing.T) {
	gw := newStatsFixture(t)
	// 未发请求：空态形状稳定。
	status, body := statsResp(t, gw, testGatewayToken)
	if status != http.StatusOK {
		t.Fatalf("empty stats = %d, want 200", status)
	}
	if len(body["stages"].([]any)) != 0 || len(body["combos"].([]any)) != 0 {
		t.Errorf("empty stats = %v/%v, want empty arrays", body["stages"], body["combos"])
	}
	// route-scoped 403；compression-scoped 200。
	if status, _ := statsResp(t, gw, DeriveMCPToken(testGatewayToken, []string{ScopeRoute})); status != http.StatusForbidden {
		t.Errorf("route-scoped stats = %d, want 403", status)
	}
	if status, _ := statsResp(t, gw, DeriveMCPToken(testGatewayToken, []string{ScopeCompression})); status != http.StatusOK {
		t.Errorf("compression-scoped stats = %d, want 200", status)
	}
}
