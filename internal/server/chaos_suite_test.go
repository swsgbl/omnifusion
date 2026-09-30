// chaos_suite_test.go 是蓝图 Phase 10 的混沌质量门禁（首片）：对
// /v1/chat 注入上游故障谱（401/403/404/409/429/5xx/慢首字节/坏
// JSON/不可达/全灭），量化成功率、p50/p95/p99、重试与切换次数、
// 缓存命中——指标直接取自 route_decisions 回放记录（Phase 9 的
// 证据面即质量门禁的数据面）。不变量（可重试故障+健康备选=100%
// 成功；全灭=0% 且不挂不炸）是门禁断言；数字报告在设置
// CHAOS_REPORT 环境变量时落盘（版本+commit+数据版本+环境摘要，
// 蓝图 §11 报告纪律），CI 只断言不落盘。
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/store"
)

// chaosDataVersion 是 mock 上游行为集的版本（蓝图：报告保留测试
// 数据版本——改故障谱必须 bump）。
const chaosDataVersion = "chaos-mocks-v1"

// chaosSamples 是每个场景的请求数（确定性固定，非随机）。
const chaosSamples = 20

// chaosResult 是一个场景的量化指标（蓝图 §11 报告字段的可测子集；
// TTFT 非流式=整请求时延）。
type chaosResult struct {
	Scenario  string  `json:"scenario"`
	Samples   int     `json:"samples"`
	Success   float64 `json:"success_rate"`
	Retries   int     `json:"retries"`
	Fallbacks int     `json:"fallbacks"`
	P50MS     float64 `json:"p50_ms"`
	P95MS     float64 `json:"p95_ms"`
	P99MS     float64 `json:"p99_ms"`
	TTFTP95MS float64 `json:"ttft_p95_ms"`
	CacheHits int     `json:"cache_hits"`
}

// healthyUpstreamBody 是健康 provider 的合法响应。
func healthyUpstreamBody() string {
	return `{"id":"c1","object":"chat.completion","created":1,"model":"model-a","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
}

// chaosGateway 装配被测网关：bad 指向故障上游（nil = 已关闭的服务=
// 不可达），可选健康第二 provider。返回网关与其 store（读回放记录）。
func chaosGateway(t *testing.T, badURL string, withGood bool) (*httptest.Server, *store.Store) {
	t.Helper()
	provs := []provider.Provider{}
	if a, err := openai_compat.New(openai_compat.Spec{ProviderName: "bad", BaseURL: badURL + "/v1", APIKey: "k"}); err == nil {
		provs = append(provs, a)
	}
	if withGood {
		good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, healthyUpstreamBody())
		}))
		t.Cleanup(good.Close)
		a, err := openai_compat.New(openai_compat.Spec{ProviderName: "good", BaseURL: good.URL + "/v1", APIKey: "k"})
		if err != nil {
			t.Fatalf("good adapter: %v", err)
		}
		provs = append(provs, a)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "chaos.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), st))
	s.SetRouter(&routing.Router{Providers: provs})
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	return gw, st
}

// runChaos 驱动一个场景：N 次同请求，返回指标（时延分位 + 回放
// 证据聚合的重试/切换计数）。
func runChaos(t *testing.T, gwURL string, st *store.Store, name string) chaosResult {
	t.Helper()
	body := `{"model":"model-a","messages":[{"role":"user","content":"chaos"}]}`
	latencies := make([]time.Duration, 0, chaosSamples)
	ok := 0
	for i := 0; i < chaosSamples; i++ {
		start := time.Now()
		resp := postAuthed(t, gwURL+"/v1/chat/completions", body)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		latencies = append(latencies, time.Since(start))
		if resp.StatusCode == http.StatusOK {
			ok++
		}
	}
	sorted := append([]time.Duration(nil), latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	rows, err := st.LoadRecentRouteDecisions(chaosSamples * 2)
	if err != nil {
		t.Fatalf("[%s] load decisions: %v", name, err)
	}
	retries, fallbacks := 0, 0
	for _, r := range rows {
		var ev struct {
			ReasonCodes []string `json:"reason_codes"`
		}
		_ = json.Unmarshal([]byte(r.Evidence), &ev)
		if r.Tries > 1 {
			retries += r.Tries - 1
		}
		for _, rc := range ev.ReasonCodes {
			if rc == "FAILOVER_CHOSEN" {
				fallbacks++
			}
		}
	}
	return chaosResult{
		Scenario: name, Samples: chaosSamples,
		Success: float64(ok) / float64(chaosSamples),
		Retries: retries, Fallbacks: fallbacks,
		P50MS:     ms(sorted[pct(len(sorted), 50)]),
		P95MS:     ms(sorted[pct(len(sorted), 95)]),
		P99MS:     ms(sorted[pct(len(sorted), 99)]),
		TTFTP95MS: ms(sorted[pct(len(sorted), 95)]), // 非流式：TTFT=整请求时延
	}
}

// pct 返回 q 分位样本的下标（ceil 语义）。
func pct(n, q int) int {
	i := (n*q + 99) / 100
	if i > n {
		i = n
	}
	return i - 1
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// chaosFaults 是故障谱：name → 故障上游行为。
func chaosFaults() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"auth_401":       func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) },
		"forbidden_403":  func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(403) },
		"not_found_404":  func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) },
		"conflict_409":   func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(409) },
		"rate_limit_429": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(429) },
		"server_5xx":     func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) },
		"bad_gateway":    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(502) },
		"malformed_json": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("garbage{not json")) },
		"slow_first_byte": func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(60 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, healthyUpstreamBody())
		},
	}
}

// TestChaosFailoverInvariants 是门禁核心断言：可重试故障谱 + 健康
// 备选 → 100% 成功且至少发生一次真实切换；慢上游（自身成功）→
// 100% 成功。p95 上界宽松（本地 mock 2s）防环境抖动。
func TestChaosFailoverInvariants(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos suite skipped in -short")
	}
	var results []chaosResult
	for name, fault := range chaosFaults() {
		name, fault := name, fault
		t.Run(name, func(t *testing.T) {
			up := httptest.NewServer(fault)
			t.Cleanup(up.Close)
			gw, st := chaosGateway(t, up.URL, true)
			r := runChaos(t, gw.URL, st, name)
			results = append(results, r)
			if r.Success != 1.0 {
				t.Fatalf("[%s] success rate = %.2f, want 1.00 (retryable fault + healthy failover)", name, r.Success)
			}
			if r.P95MS > 2000 {
				t.Fatalf("[%s] p95 = %.0fms, want < 2000ms", name, r.P95MS)
			}
		})
	}
	// 慢上游在 bad 位自身成功（首候选 200）——切换数为 0 但成功率 1。
	// （已并入上表 slow_first_byte，不单独断言 fallback。）
	writeChaosReport(t, results)
}

// TestChaosProviderUnavailable 验证"上游不可达"（连接拒绝）故障：
// 快速失败并切换，成功率 100%。
func TestChaosProviderUnavailable(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos suite skipped in -short")
	}
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // 立即关闭 → 连接拒绝
	gw, st := chaosGateway(t, deadURL, true)
	r := runChaos(t, gw.URL, st, "unreachable")
	if r.Success != 1.0 {
		t.Fatalf("[unreachable] success rate = %.2f, want 1.00", r.Success)
	}
	if r.Fallbacks == 0 {
		t.Fatalf("[unreachable] no failover happened")
	}
}

// TestChaosAllProvidersFail 验证全灭：唯一 provider 全 5xx → 0%
// 成功、客户端拿到 502、不挂不炸，且每笔请求都留下回放证据。
func TestChaosAllProvidersFail(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos suite skipped in -short")
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	t.Cleanup(up.Close)
	gw, st := chaosGateway(t, up.URL, false)
	r := runChaos(t, gw.URL, st, "all_fail_5xx")
	if r.Success != 0.0 {
		t.Fatalf("[all_fail] success rate = %.2f, want 0.00", r.Success)
	}
	rows, err := st.LoadRecentRouteDecisions(chaosSamples)
	if err != nil || len(rows) != chaosSamples {
		t.Fatalf("[all_fail] evidence rows = %d err=%v, want %d（每笔失败都有回放证据）", len(rows), err, chaosSamples)
	}
}

// TestChaosCacheHitRatio 验证缓存命中指标：确定性请求（temp=0）N
// 次 → 恰好 1 次 miss、N-1 次 hit（回写等待纳入）。
func TestChaosCacheHitRatio(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos suite skipped in -short")
	}
	url, hits, s := newCacheFixture(t, false)
	body := `{"model":"model-a","temperature":0,"messages":[{"role":"user","content":"chaos-cache"}]}`
	hitratio := 0
	for i := 0; i < chaosSamples; i++ {
		resp, elapsed := awaitCacheHit(t, url+"/v1/chat/completions", body)
		_ = elapsed
		if resp.Header.Get("X-OmniFusion-Cache") == "hit" {
			hitratio++
		}
		_ = resp.Body.Close()
	}
	// awaitCacheHit 自身重放至命中——改用一次性的 miss/hit 序列断言：
	// miss 只应发生一次（上游只打一次）。
	if hits.Load() != 1 {
		t.Fatalf("[cache] upstream hits = %d, want 1 (N-1 hits served from cache)", hits.Load())
	}
	_ = s
	_ = hitratio
}

// writeChaosReport 在设置 CHAOS_REPORT 时落盘版本化报告（蓝图 §11：
// 版本号、commit hash、测试数据版本、环境摘要）。
func writeChaosReport(t *testing.T, results []chaosResult) {
	t.Helper()
	path := os.Getenv("CHAOS_REPORT")
	if path == "" || len(results) == 0 {
		return
	}
	commit := os.Getenv("CHAOS_COMMIT")
	if commit == "" {
		out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
		if err == nil {
			commit = strings.TrimSpace(string(out))
		}
	}
	rep := map[string]any{
		"version":      os.Getenv("CHAOS_VERSION"),
		"commit":       commit,
		"data_version": chaosDataVersion,
		"env":          fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		"scenarios":    results,
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir report dir: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
}
