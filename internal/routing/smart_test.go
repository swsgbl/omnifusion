// smart_test.go 覆盖 M6.3 ML 路由路径：WithSmart 配置位、计划成员
// 解析（顺序/未装配跳过/窗口过滤）、无 sticky 无钉选语义、Smart 未
// 装配回退、Dispatch 级 failover（主档失败换另一档）。
package routing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
)

func TestWithSmartSetsConfig(t *testing.T) {
	cfg := resolveOptions([]DispatchOption{WithSmart()})
	if !cfg.smart {
		t.Fatal("WithSmart() 未置位 cfg.smart")
	}
}

// threeStubProviders 起注册序 a/b/c 三个 stub（仅候选解析用，不出站）。
func threeStubProviders(t *testing.T) []provider.Provider {
	t.Helper()
	return []provider.Provider{
		&stubProvider{name: "a"},
		&stubProvider{name: "b"},
		&stubProvider{name: "c"},
	}
}

// smartRouter 起一个三 provider 路由器（a/b/c 注册序）+ 固定弱/强计划。
func smartRouter(t *testing.T, plan SmartPlan) *Router {
	t.Helper()
	return &Router{
		Providers: threeStubProviders(t),
		Smart:     func(*schema.UnifiedRequest) SmartPlan { return plan },
	}
}

func TestSmartCandidatesPlanOrder(t *testing.T) {
	r := smartRouter(t, SmartPlan{
		Tier: "weak",
		Members: []ComboMember{
			{Provider: "a", Model: "m-weak"},
			{Provider: "b", Model: "m-strong"},
		},
	})
	cands := r.candidatesFor(dispatchConfig{smart: true}, testRequest())
	if len(cands) != 2 {
		t.Fatalf("候选数 = %d, want 2", len(cands))
	}
	if cands[0].p.Name() != "a" || cands[0].model != "m-weak" {
		t.Errorf("首位 = %s/%s, want a/m-weak", cands[0].p.Name(), cands[0].model)
	}
	if cands[1].p.Name() != "b" || cands[1].model != "m-strong" {
		t.Errorf("次位 = %s/%s, want b/m-strong（另一档殿后）", cands[1].p.Name(), cands[1].model)
	}
}

func TestSmartCandidatesSkipUnconfiguredMember(t *testing.T) {
	r := smartRouter(t, SmartPlan{
		Tier: "strong",
		Members: []ComboMember{
			{Provider: "ghost", Model: "x"}, // 未装配：跳过
			{Provider: "b", Model: "m-strong"},
		},
	})
	cands := r.candidatesFor(dispatchConfig{smart: true}, testRequest())
	if len(cands) != 1 || cands[0].p.Name() != "b" {
		t.Fatalf("候选 = %v, want 仅 b", cands)
	}
}

func TestSmartCandidatesWindowFilter(t *testing.T) {
	r := smartRouter(t, SmartPlan{
		Tier: "weak",
		Members: []ComboMember{
			{Provider: "a", Model: "m-weak"},
			{Provider: "b", Model: "m-strong"},
		},
	})
	r.Windows = stubWindows{"a/m-weak": 100} // 弱档窗口装不下 200 token
	cands := r.candidatesFor(dispatchConfig{smart: true, promptTokens: 200}, testRequest())
	if len(cands) != 1 || cands[0].p.Name() != "b" {
		t.Fatalf("候选 = %v, want 弱档被窗口过滤只剩 b", cands)
	}
}

func TestSmartIgnoresStickyAndPin(t *testing.T) {
	r := smartRouter(t, SmartPlan{
		Tier: "weak",
		Members: []ComboMember{
			{Provider: "a", Model: "m-weak"},
			{Provider: "b", Model: "m-strong"},
		},
	})
	r.Sessions = NewSessionTracker()
	r.Sessions.Bind("sess", "b") // 已绑 b：sticky 本会提前 b
	cfg := dispatchConfig{smart: true, sessionID: "sess", pinnedProvider: "b"}
	cands := r.candidatesFor(cfg, testRequest())
	if len(cands) != 2 || cands[0].p.Name() != "a" {
		t.Fatalf("候选 = %v, want 计划序 a 在首（smart 不做 sticky/钉选）", cands)
	}
}

func TestSmartNilFallsBackToModelRouting(t *testing.T) {
	r := &Router{Providers: threeStubProviders(t)} // Smart 未装配
	cands := r.candidatesFor(dispatchConfig{smart: true}, testRequest())
	if len(cands) != 3 || cands[0].p.Name() != "a" {
		t.Fatalf("候选 = %v, want 回退普通分发（注册序 3 家）", cands)
	}
}

// TestSmartDispatchFailover：弱档上游 500 → 主循环自然换强档；
// 隔离/打分等机制不因 smart 路径失位。
func TestSmartDispatchFailover(t *testing.T) {
	var hits []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		if strings.Contains(s, "m-weak") {
			hits = append(hits, "m-weak")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"message":"boom"}}`)
			return
		}
		hits = append(hits, "m-strong")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, okCompletion("m-strong"))
	}))
	t.Cleanup(up.Close)

	r := &Router{Providers: []provider.Provider{
		newMockAdapter(t, "a", up.URL),
		newMockAdapter(t, "b", up.URL),
		newMockAdapter(t, "c", up.URL),
	}}
	r.Smart = func(*schema.UnifiedRequest) SmartPlan {
		return SmartPlan{Tier: "weak", Members: []ComboMember{
			{Provider: "a", Model: "m-weak"},
			{Provider: "b", Model: "m-strong"},
		}}
	}
	resp, attempts, err := r.Dispatch(context.Background(), testRequest(), WithSmart())
	if err != nil {
		t.Fatalf("Dispatch: %v（attempts=%+v）", err, attempts)
	}
	if resp.Model != "m-strong" {
		t.Errorf("终稿 model = %q, want m-strong（弱档失败 failover）", resp.Model)
	}
	if len(hits) != 2 || hits[0] != "m-weak" || hits[1] != "m-strong" {
		t.Errorf("尝试序 = %v, want [m-weak, m-strong]", hits)
	}
}
