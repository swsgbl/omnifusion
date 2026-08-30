// pin_test.go 验证 全局路由钉选：钉选 provider 提到尝试序列
// 首位（压过策略排序与 sticky），不在候选则忽略；组合路径不受影响；
// Isolation.Clear 人工清除隔离后 provider 立即回到候选。
package routing

import (
	"context"
	"testing"
	"time"
)

// newMemIsolation 装配纯内存隔离状态机（无持久化）。
func newMemIsolation(t *testing.T) *Isolation {
	t.Helper()
	iso, err := NewIsolation(nil, nil)
	if err != nil {
		t.Fatalf("NewIsolation: %v", err)
	}
	return iso
}

// TestPinnedProviderOverridesStrategy 验收钉选压过打分排序：策略序
// 偏向 b（a 变慢），但 pin a 后分发先打 a。
func TestPinnedProviderOverridesStrategy(t *testing.T) {
	r := newScoringRouter(t, "a", "b")
	r.Scoring.Observe("a", 3*time.Second, true) // a 慢 → 策略序 b 在前
	if got := r.ordered("", "", 0); got[0].Name() != "b" {
		t.Fatalf("auto order = [%s ...], want b first", got[0].Name())
	}

	resp, _, err := r.Dispatch(context.Background(), testRequest(), WithPinnedProvider("a"))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if resp.ProviderName != "a" {
		t.Fatalf("dispatch provider = %q, want pinned a", resp.ProviderName)
	}
}

// TestPinnedProviderOverridesSticky 验收钉选压过 sticky session：会话
// 已粘住 b，pin a 后同会话也先打 a（运维显式意图最高优先）。
func TestPinnedProviderOverridesSticky(t *testing.T) {
	r := newSessionRouter(t)

	resp, _, err := r.Dispatch(context.Background(), testRequest(), WithSession("s1"))
	if err != nil {
		t.Fatalf("dispatch 1: %v", err)
	}
	if resp.ProviderName != "a" {
		t.Fatalf("dispatch 1 provider = %q, want a (register order)", resp.ProviderName)
	}
	r.Scoring.Observe("a", 3*time.Second, true)
	resp, _, err = r.Dispatch(context.Background(), testRequest(), WithSession("s1"))
	if err != nil {
		t.Fatalf("dispatch 2: %v", err)
	}
	if resp.ProviderName != "a" {
		t.Fatalf("dispatch 2 provider = %q, want a (sticky)", resp.ProviderName)
	}

	resp, _, err = r.Dispatch(context.Background(), testRequest(),
		WithSession("s1"), WithPinnedProvider("b"))
	if err != nil {
		t.Fatalf("dispatch 3 (pinned b): %v", err)
	}
	if resp.ProviderName != "b" {
		t.Fatalf("dispatch 3 provider = %q, want pinned b over sticky a", resp.ProviderName)
	}
}

// TestPinnedProviderAbsentIgnored 验收钉选不引入新候选：钉一个未装配
// 的 provider 是 no-op，分发照常。
func TestPinnedProviderAbsentIgnored(t *testing.T) {
	r := newScoringRouter(t, "a", "b")
	resp, _, err := r.Dispatch(context.Background(), testRequest(), WithPinnedProvider("ghost"))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if resp.ProviderName != "a" {
		t.Fatalf("dispatch provider = %q, want a (pin ignored)", resp.ProviderName)
	}
}

// TestPinKeepsFailover 验收钉选保留 failover：钉选 provider 失败时
// 仍按序尝试其余候选（钉选是重排不是过滤）。
func TestPinKeepsFailover(t *testing.T) {
	r := newScoringRouter(t, "a", "b")
	r.Isolation = newMemIsolation(t)
	r.Isolation.ApplyFailure("a", "m", KindRateLimit) // a Connection 冷却 30s
	resp, _, err := r.Dispatch(context.Background(), testRequest(), WithPinnedProvider("a"))
	if err != nil {
		t.Fatalf("dispatch with pinned provider cooling: %v", err)
	}
	if resp.ProviderName != "b" {
		t.Fatalf("dispatch provider = %q, want b (failover past isolated pin)", resp.ProviderName)
	}
}

// TestIsolationClearResumesProvider 验收 Clear 人工清除：冷却中的
// provider 清除后立即回到候选首位（钉选场景）。
func TestIsolationClearResumesProvider(t *testing.T) {
	r := newScoringRouter(t, "a", "b")
	r.Isolation = newMemIsolation(t)
	r.Isolation.ApplyFailure("a", "m", KindRateLimit)
	if blocked, _ := r.Isolation.Block("a"); !blocked {
		t.Fatalf("provider a should be cooling before clear")
	}

	r.Isolation.Clear("a")

	if blocked, _ := r.Isolation.Block("a"); blocked {
		t.Fatalf("provider a should be clear after Clear")
	}
	resp, _, err := r.Dispatch(context.Background(), testRequest(), WithPinnedProvider("a"))
	if err != nil {
		t.Fatalf("dispatch after clear: %v", err)
	}
	if resp.ProviderName != "a" {
		t.Fatalf("dispatch provider = %q, want a after isolation cleared", resp.ProviderName)
	}
}
