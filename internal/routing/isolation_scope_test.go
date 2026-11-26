// isolation_scope_test.go 覆盖 2026-11-25 "应用用不了" 事故的四条
// 隔离粒度规则——每条都是日志里真实出现过的失败形态：
//
//	免密实例的 401 只锁模型（Kilo 旧免费模型转收费 → 整家被隔离 30 分钟）
//	403 配额不足只锁模型（Agnes 付费模型 403 → 同家免费模型 0ms 502）
//	聚合面的 429 只锁模型（OpenRouter 单模型上游限流 → 整家冷却）
//	404/410 锁模型（NVIDIA 已 410 的模型每个请求都被重试）
package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
)

// poolFix 是可配置的能力/定价源：每家一个"最强模型"与一份免费清单。
type poolFix struct {
	best map[string]string
	free map[string][]string
}

func (f poolFix) Capability(providerName, _ string) (float64, bool) {
	if _, ok := f.best[providerName]; ok {
		return 90, true
	}
	return 0, false
}
func (f poolFix) BestModel(p string) (string, float64, bool) {
	m, ok := f.best[p]
	return m, 90, ok
}
func (f poolFix) Price(providerName, model string) (provider.Price, bool) {
	for _, id := range f.free[providerName] {
		if id == model {
			return provider.Price{}, true
		}
	}
	return provider.Price{}, false
}
func (f poolFix) Cheapest(string) (provider.Price, bool) { return provider.Price{}, true }
func (f poolFix) CheapestModel(p string) (string, provider.Price, bool) {
	if len(f.free[p]) == 0 {
		return "", provider.Price{}, false
	}
	return f.free[p][0], provider.Price{}, true
}
func (f poolFix) FreeModels(p string) []string { return f.free[p] }

// statusUpstream 按请求里的模型回状态码：命中的模型回 status + 一段
// 错误体（quota=true 时带配额关键词），其余回正常补全。hits 记录
// 每个模型被真实请求的次数。
func statusUpstream(t *testing.T, status int, quota bool, hits map[string]int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		hits[body.Model]++
		if strings.HasPrefix(body.Model, "bad-") {
			w.WriteHeader(status)
			if quota {
				io.WriteString(w, `{"error":{"message":"Insufficient user quota, remaining: -0.000098"}}`)
			} else {
				io.WriteString(w, `{"error":{"message":"rejected"}}`)
			}
			return
		}
		io.WriteString(w, okCompletion(body.Model))
	}))
}

func newSpecAdapter(t *testing.T, spec openai_compat.Spec) provider.Provider {
	t.Helper()
	a, err := openai_compat.New(spec)
	if err != nil {
		t.Fatalf("New(%s): %v", spec.ProviderName, err)
	}
	return a
}

func newIsolatedRouter(t *testing.T, p provider.Provider, fx poolFix) (*Router, *Isolation) {
	t.Helper()
	iso, err := NewIsolation(nil, nil)
	if err != nil {
		t.Fatalf("NewIsolation: %v", err)
	}
	return &Router{
		Providers:  []provider.Provider{p},
		Isolation:  iso,
		Capability: fx,
		Price:      fx,
	}, iso
}

func autoRequest() *schema.UnifiedRequest {
	return &schema.UnifiedRequest{Model: "", Messages: []schema.Message{
		{Role: "user", Content: schema.NewTextContent("hi")},
	}}
}

// assertRecovers 断言第一次分发的赢家是安全网的 good 模型、第二次分发
// 不再碰坏模型、且该家没有被整家隔离。
func assertRecovers(t *testing.T, r *Router, iso *Isolation, name, bad string, hits map[string]int) {
	t.Helper()
	resp, attempts, err := r.Dispatch(context.Background(), autoRequest(), WithQualityAuto())
	if err != nil {
		t.Fatalf("Dispatch: %v (attempts %+v)", err, attempts)
	}
	if resp.ProviderName != name || resp.Model != "good-free" {
		t.Fatalf("winner = %s/%s, want %s/good-free (attempts %+v)", resp.ProviderName, resp.Model, name, attempts)
	}
	if blocked, reason := iso.Block(name); blocked {
		t.Fatalf("%s must NOT be cooled down as a provider, got %q", name, reason)
	}
	if locked, _ := iso.LockedModel(name, bad); !locked {
		t.Fatalf("model %s/%s should be locked", name, bad)
	}
	if _, _, err := r.Dispatch(context.Background(), autoRequest(), WithQualityAuto()); err != nil {
		t.Fatalf("second Dispatch: %v", err)
	}
	if hits[bad] != 1 {
		t.Errorf("bad model %s was hit %d times across two requests, want 1 (locked afterwards)", bad, hits[bad])
	}
}

// TestKeylessAuthFailureIsModelScoped 免密实例（Kilo）上一个转收费的旧
// 模型回 401：只锁该模型，同家免费模型照常接住。
func TestKeylessAuthFailureIsModelScoped(t *testing.T) {
	hits := map[string]int{}
	up := statusUpstream(t, http.StatusUnauthorized, false, hits)
	defer up.Close()
	kilo := newSpecAdapter(t, openai_compat.Spec{
		ProviderName: "kilo", BaseURL: up.URL + "/v1", Aggregator: true, // 无 key
	})
	r, iso := newIsolatedRouter(t, kilo, poolFix{
		best: map[string]string{"kilo": "bad-paid"},
		free: map[string][]string{"kilo": {"good-free"}},
	})
	assertRecovers(t, r, iso, "kilo", "bad-paid", hits)
}

// TestKeyedAuthFailureStillCoolsProvider 常规回归门：带 key 的实例 401
// 仍是凭据失效——整家长冷却，后续请求直接跳过不再打上游。
func TestKeyedAuthFailureStillCoolsProvider(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
	}))
	defer up.Close()
	zhipu := newSpecAdapter(t, openai_compat.Spec{ProviderName: "zhipu", BaseURL: up.URL + "/v1", APIKey: "k"})
	r, iso := newIsolatedRouter(t, zhipu, poolFix{
		best: map[string]string{"zhipu": "bad-best"},
		free: map[string][]string{"zhipu": {"bad-free"}},
	})
	if _, _, err := r.Dispatch(context.Background(), autoRequest(), WithQualityAuto()); err == nil {
		t.Fatal("dead key must fail the request")
	}
	if blocked, reason := iso.Block("zhipu"); !blocked || !strings.Contains(reason, "cooldown") {
		t.Fatalf("keyed auth failure must cool the provider down, got %v %q", blocked, reason)
	}
	first := calls
	if _, _, err := r.Dispatch(context.Background(), autoRequest(), WithQualityAuto()); err == nil {
		t.Fatal("cooled-down provider must still fail")
	}
	if calls != first {
		t.Fatalf("cooled-down provider was hit again (%d → %d calls)", first, calls)
	}
}

// TestQuota403IsModelScoped Agnes 形态：付费模型 403 "Insufficient user
// quota" 按配额锁定该模型，同家免费模型不受牵连。
func TestQuota403IsModelScoped(t *testing.T) {
	hits := map[string]int{}
	up := statusUpstream(t, http.StatusForbidden, true, hits)
	defer up.Close()
	agnes := newSpecAdapter(t, openai_compat.Spec{ProviderName: "agnes", BaseURL: up.URL + "/v1", APIKey: "k"})
	r, iso := newIsolatedRouter(t, agnes, poolFix{
		best: map[string]string{"agnes": "bad-pro"},
		free: map[string][]string{"agnes": {"good-free"}},
	})
	assertRecovers(t, r, iso, "agnes", "bad-pro", hits)
}

// TestAggregatorRateLimitIsModelScoped OpenRouter 形态：单模型 429（无
// 配额关键词）只锁该模型 30s；非聚合面家的 429 仍冷却整家。
func TestAggregatorRateLimitIsModelScoped(t *testing.T) {
	hits := map[string]int{}
	up := statusUpstream(t, http.StatusTooManyRequests, false, hits)
	defer up.Close()
	orr := newSpecAdapter(t, openai_compat.Spec{
		ProviderName: "openrouter", BaseURL: up.URL + "/v1", APIKey: "k", Aggregator: true,
	})
	r, iso := newIsolatedRouter(t, orr, poolFix{
		best: map[string]string{"openrouter": "bad-hot"},
		free: map[string][]string{"openrouter": {"good-free"}},
	})
	assertRecovers(t, r, iso, "openrouter", "bad-hot", hits)
	if locked, _ := iso.LockedModel("openrouter", "bad-hot"); !locked {
		t.Fatal("rate-limited model should be locked")
	}

	// 非聚合面：账户级限流，整家冷却（行为不变）——同一家里的好模型
	// 也被跳过，这正是聚合面要区别对待的原因。
	hits2 := map[string]int{}
	up2 := statusUpstream(t, http.StatusTooManyRequests, false, hits2)
	defer up2.Close()
	deepseek := newSpecAdapter(t, openai_compat.Spec{ProviderName: "deepseek", BaseURL: up2.URL + "/v1", APIKey: "k"})
	r2, iso2 := newIsolatedRouter(t, deepseek, poolFix{
		best: map[string]string{"deepseek": "bad-hot"},
		free: map[string][]string{"deepseek": {"good-free"}},
	})
	if _, _, err := r2.Dispatch(context.Background(), autoRequest(), WithQualityAuto()); err == nil {
		t.Fatal("account-wide rate limit must fail the request (provider cooled down)")
	}
	if blocked, _ := iso2.Block("deepseek"); !blocked {
		t.Fatal("non-aggregator rate limit must cool the provider down")
	}
	if hits2["good-free"] != 0 {
		t.Fatalf("good-free was tried on a cooled-down provider (%d hits)", hits2["good-free"])
	}
}

// TestModelGoneIsLocked 已下线模型（410 Gone）锁 10 分钟：锁期内不再
// 重试，到点解锁。
func TestModelGoneIsLocked(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusGone} {
		hits := map[string]int{}
		up := statusUpstream(t, status, false, hits)
		nv := newSpecAdapter(t, openai_compat.Spec{ProviderName: "nvidia", BaseURL: up.URL + "/v1", APIKey: "k"})
		r, iso := newIsolatedRouter(t, nv, poolFix{
			best: map[string]string{"nvidia": "bad-retired"},
			free: map[string][]string{"nvidia": {"good-free"}},
		})
		assertRecovers(t, r, iso, "nvidia", "bad-retired", hits)
		up.Close()
	}

	iso, fc := newTestIsolation(t)
	iso.ApplyFailure("nvidia", "m", KindModelGone)
	fc.Add(ModelGoneLockout - time.Second)
	if locked, _ := iso.LockedModel("nvidia", "m"); !locked {
		t.Fatal("model_gone lock must hold for ModelGoneLockout")
	}
	fc.Add(time.Second)
	if locked, _ := iso.LockedModel("nvidia", "m"); locked {
		t.Fatal("model_gone lock must expire after ModelGoneLockout")
	}
	if blocked, _ := iso.Block("nvidia"); blocked {
		t.Fatal("model_gone must not cool the provider down")
	}
	// 配额类仍是 1h（时长按类别区分）。
	iso.ApplyFailure("d", "m2", KindQuotaExhausted)
	fc.Add(time.Hour - time.Second)
	if locked, _ := iso.LockedModel("d", "m2"); !locked {
		t.Fatal("quota lock must last an hour")
	}
}

// TestDialFailureTripsBreakerFast 建连失败两次即熔断（主机不可达几乎
// 不自愈，不等常规窗口阈值），且沿用指数退避。
func TestDialFailureTripsBreakerFast(t *testing.T) {
	iso, fc := newTestIsolation(t)
	iso.ApplyDialFailure("huggingface")
	if blocked, _ := iso.Block("huggingface"); blocked {
		t.Fatal("one dial failure must not trip the breaker (transient blip)")
	}
	iso.ApplyDialFailure("huggingface")
	if blocked, reason := iso.Block("huggingface"); !blocked || !strings.Contains(reason, "breaker open") {
		t.Fatalf("two consecutive dial failures must trip, got %v %q", blocked, reason)
	}
	fc.Add(30 * time.Second) // 首次熔断 30s 到点 → 放行探测
	if blocked, _ := iso.Block("huggingface"); blocked {
		t.Fatal("breaker should be half-open after the base window")
	}
	iso.ApplyDialFailure("huggingface") // 探测仍连不上 → 退避翻倍
	fc.Add(31 * time.Second)
	if blocked, _ := iso.Block("huggingface"); !blocked {
		t.Fatal("failed probe must re-open with doubled backoff (60s)")
	}
	fc.Add(60 * time.Second) // 第二次熔断（60s）到点 → 放行探测
	if blocked, _ := iso.Block("huggingface"); blocked {
		t.Fatal("breaker should be half-open after the doubled window")
	}
	iso.ApplySuccess("huggingface") // 探测成功 → 闭合
	if blocked, _ := iso.Block("huggingface"); blocked {
		t.Fatal("breaker must close after a successful probe")
	}
	iso.ApplyDialFailure("huggingface") // 连败计数已清零：单次失败不熔断
	if blocked, _ := iso.Block("huggingface"); blocked {
		t.Fatal("dial streak must restart from zero after success")
	}
}

// TestIsDialError 只认 typed error：*net.OpError 的 dial 阶段算建连
// 失败；建连之后的超时/取消不算。
func TestIsDialError(t *testing.T) {
	dial := &url.Error{Op: "Post", URL: "https://up.local/v1", Err: &net.OpError{
		Op: "dial", Net: "tcp", Err: errors.New("connection refused"),
	}}
	dialTimeout := fmt.Errorf("upstream transport: %w", &url.Error{Op: "Post", URL: "https://up.local/v1", Err: &net.OpError{
		Op: "dial", Net: "tcp", Err: context.DeadlineExceeded,
	}})
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"dial refused", dial, true},
		{"dial timeout wrapped", dialTimeout, true},
		{"post-connect deadline", fmt.Errorf("upstream transport: %w", &url.Error{Op: "Post", Err: context.DeadlineExceeded}), false},
		{"client canceled", context.Canceled, false},
		{"upstream 429", &provider.UpstreamError{Status: 429}, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := isDialError(tc.err); got != tc.want {
			t.Errorf("%s: isDialError = %v, want %v", tc.name, got, tc.want)
		}
	}
}
