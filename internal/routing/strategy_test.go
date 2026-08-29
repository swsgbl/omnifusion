package routing

import (
	"context"
	"testing"
	"time"
)

func TestParseModelDirective(t *testing.T) {
	cases := []struct {
		in       string
		strategy string
		bare     string
		wantErr  bool
	}{
		{"llama-3.3-70b", "", "llama-3.3-70b", false},
		{"@fast:llama-3.3-70b", "fast", "llama-3.3-70b", false},
		{"@cheap", "cheap", "", false},
		{"@auto:gpt-4o", "auto", "gpt-4o", false},
		{"@bogus:model", "", "", true},
		{"@:", "", "", true},
	}
	for _, c := range cases {
		gotStrategy, gotBare, err := ParseModelDirective(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseModelDirective(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if gotStrategy != c.strategy || gotBare != c.bare {
			t.Errorf("ParseModelDirective(%q) = %q/%q, want %q/%q", c.in, gotStrategy, gotBare, c.strategy, c.bare)
		}
	}
}

// TestBuiltinStrategyOrders 逐一验证五个内置策略的排序语义。
func TestBuiltinStrategyOrders(t *testing.T) {
	r := newScoringRouter(t, "a", "b")

	// priority：无论信号如何，保持注册序。
	r.Scoring.Observe("a", 3*time.Second, false)
	if got := r.ordered("priority", "", 0); got[0].Name() != "a" {
		t.Fatalf("priority rank = [%s ...], want a first", got[0].Name())
	}

	// fast：延迟 EWMA 升序。
	r2 := newScoringRouter(t, "a", "b")
	r2.Scoring.Observe("a", 3*time.Second, true)
	r2.Scoring.Observe("b", 10*time.Millisecond, true)
	if got := r2.ordered("fast", "", 0); got[0].Name() != "b" {
		t.Fatalf("fast rank = [%s ...], want b first", got[0].Name())
	}

	// cheap：配额余量降序（a 半满，b 未设限）。
	r3 := newScoringRouter(t, "a", "b")
	qt := NewQuotaTracker()
	qt.SetLimit("a", QuotaLimits{RPM: 2})
	qt.RecordRequest("a")
	r3.Quota = qt
	if got := r3.ordered("cheap", "", 0); got[0].Name() != "b" {
		t.Fatalf("cheap rank = [%s ...], want b first", got[0].Name())
	}

	// lkgp：最近成功者优先（b 晚于 a 成功）。
	r4 := newScoringRouter(t, "a", "b")
	fc := newFakeClock()
	r4.Scoring.now = fc.Now
	r4.Scoring.Observe("a", time.Millisecond, true)
	fc.Add(time.Minute)
	r4.Scoring.Observe("b", time.Millisecond, true)
	if got := r4.ordered("lkgp", "", 0); got[0].Name() != "b" {
		t.Fatalf("lkgp rank = [%s ...], want b first (most recent success)", got[0].Name())
	}

	// lkgp：从未成功者殿后，成功者在前。
	r5 := newScoringRouter(t, "a", "b")
	r5.Scoring.Observe("b", time.Millisecond, true)
	if got := r5.ordered("lkgp", "", 0); got[0].Name() != "b" {
		t.Fatalf("lkgp rank = [%s ...], want b first (only successful)", got[0].Name())
	}
}

// TestDispatchWithStrategyOption 经 Dispatch 全链路验证策略选项：
// 预置 a 慢 b 快的延迟观测后，WithStrategyName("fast") 应先打 b。
func TestDispatchWithStrategyOption(t *testing.T) {
	r := newScoringRouter(t, "a", "b")
	r.Scoring.Observe("a", 3*time.Second, true)
	r.Scoring.Observe("b", 10*time.Millisecond, true)

	resp, attempts, err := r.Dispatch(context.Background(), testRequest(), WithStrategyName("fast"))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if resp.ProviderName != "b" {
		t.Fatalf("provider = %q, want b", resp.ProviderName)
	}
	if len(attempts) != 1 || attempts[0].Provider != "b" {
		t.Fatalf("attempts = %+v, want single b attempt", attempts)
	}
}
