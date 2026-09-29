package quota

import (
	"testing"
	"time"
)

// 账本契约测试：蓝图 Phase 3 验收点——过期、冲突来源、低置信度、
// 配额重置（429 观测）、人工停用不被观测翻案。

func TestUnknownByDefault(t *testing.T) {
	l := NewLedger()
	e := l.Get("openrouter", "some-model")
	if e.State != StateUnknown {
		t.Fatalf("default = %s, want UNKNOWN", e.State)
	}
	if e.Window.Remaining != -1 {
		t.Errorf("unknown remaining = %v, want -1 (unknown ≠ 0 ≠ plenty)", e.Window.Remaining)
	}
}

func TestConflictHigherSourceWins(t *testing.T) {
	l := NewLedger()
	now := time.Now()
	// 静态声明 free → 运行时付费证据翻案
	l.Record(Entitlement{Provider: "groq", Model: "m", State: StateVerifiedFree,
		Source: SourceStaticCatalog, ObservedAt: now})
	l.Record(Entitlement{Provider: "groq", Model: "m", State: StateVerifiedPaid,
		Source: SourceRuntime429, ObservedAt: now})
	if e := l.Get("groq", "m"); e.State != StateVerifiedPaid {
		t.Fatalf("state = %s, want VERIFIED_PAID (runtime beats static)", e.State)
	}
	if e := l.Get("groq", "m"); e.Confidence != 0.9 {
		t.Errorf("confidence = %v, want 0.9 (runtime default)", e.Confidence)
	}
}

func TestStaleExpiryDegrades(t *testing.T) {
	l := NewLedger()
	past := time.Now().Add(-2 * time.Hour)
	l.Record(Entitlement{Provider: "openrouter", Model: "m", State: StateVerifiedFree,
		Source: SourceActiveProbe, ObservedAt: past, ValidUntil: past.Add(1 * time.Hour)})
	e := l.Get("openrouter", "m")
	if e.State != StateExpired {
		t.Fatalf("state = %s, want EXPIRED (past valid_until)", e.State)
	}
	if e.Window.Remaining != -1 {
		t.Errorf("expired remaining = %v, want -1 (stale data must not pose as fact)", e.Window.Remaining)
	}
	// 降级写回：再取仍是 EXPIRED
	if e2 := l.Get("openrouter", "m"); e2.State != StateExpired {
		t.Fatalf("degraded state must persist, got %s", e2.State)
	}
}

func TestQuotaExhausted429(t *testing.T) {
	l := NewLedger()
	now := time.Now()
	l.RecordQuotaExhausted("cerebras", "m", now)
	e := l.Get("cerebras", "m")
	// 429 = 窗口耗尽而非权益消失：state 保持 FREE、余量 0
	if e.State != StateVerifiedFree {
		t.Fatalf("state = %s, want VERIFIED_FREE (exhaustion is window semantics)", e.State)
	}
	if e.Window.Remaining != 0 {
		t.Errorf("remaining = %v, want 0", e.Window.Remaining)
	}
	if e.EvidenceID == "" {
		t.Error("free conclusion must carry evidence id")
	}
}

func TestDisabledNotOverriddenByObservation(t *testing.T) {
	l := NewLedger()
	now := time.Now()
	l.Disable("mistral", "m", "terms violation")
	// 运行时观测试图翻案 → 不允许（仅 Manual 可）
	l.RecordQuotaExhausted("mistral", "m", now)
	if e := l.Get("mistral", "m"); e.State != StateDisabled {
		t.Fatalf("state = %s, want DISABLED (observation cannot override policy)", e.State)
	}
	// Manual 可翻案
	l.Record(Entitlement{Provider: "mistral", Model: "m", State: StateVerifiedFree,
		Source: SourceManual, ObservedAt: now})
	if e := l.Get("mistral", "m"); e.State != StateVerifiedFree {
		t.Fatalf("manual override failed: %s", e.State)
	}
}

func TestStaleEvidenceLosesToFresher(t *testing.T) {
	l := NewLedger()
	old := time.Now().Add(-1 * time.Hour)
	now := time.Now()
	// 一小时前的 429（30 分钟时效已过）→ 已过期，不该压住刚刚成功的用量
	l.Record(Entitlement{Provider: "p", Model: "m", State: StateVerifiedPaid,
		Source: SourceRuntime429, ObservedAt: old, ValidUntil: old.Add(30 * time.Minute)})
	l.Record(Entitlement{Provider: "p", Model: "m", State: StateVerifiedFree,
		Source: SourceRuntimeUsage, ObservedAt: now})
	if e := l.Get("p", "m"); e.State != StateVerifiedFree {
		t.Fatalf("state = %s, want VERIFIED_FREE (stale 429 loses to fresh usage)", e.State)
	}
}

func TestFreshHighPriorityStillWins(t *testing.T) {
	l := NewLedger()
	now := time.Now()
	// 双双未过期：429（优先级 80）仍胜 runtime usage（70）
	l.Record(Entitlement{Provider: "p", Model: "m", State: StateVerifiedFree,
		Source: SourceRuntimeUsage, ObservedAt: now, ValidUntil: now.Add(time.Hour)})
	l.Record(Entitlement{Provider: "p", Model: "m", State: StateVerifiedPaid,
		Source: SourceRuntime429, ObservedAt: now, ValidUntil: now.Add(time.Hour)})
	if e := l.Get("p", "m"); e.State != StateVerifiedPaid {
		t.Fatalf("state = %s, want VERIFIED_PAID (fresh 429 outranks fresh usage)", e.State)
	}
}

func TestSnapshotSorted(t *testing.T) {
	l := NewLedger()
	now := time.Now()
	l.Record(Entitlement{Provider: "zeta", Model: "b", State: StateVerifiedFree, Source: SourceStaticCatalog, ObservedAt: now})
	l.Record(Entitlement{Provider: "alpha", Model: "y", State: StateVerifiedFree, Source: SourceStaticCatalog, ObservedAt: now})
	l.Record(Entitlement{Provider: "alpha", Model: "x", State: StateVerifiedFree, Source: SourceStaticCatalog, ObservedAt: now})
	sn := l.Snapshot()
	if len(sn) != 3 {
		t.Fatalf("snapshot len = %d", len(sn))
	}
	if sn[0].Provider != "alpha" || sn[0].Model != "x" || sn[1].Model != "y" || sn[2].Provider != "zeta" {
		t.Errorf("snapshot not sorted: %+v", sn)
	}
}

func TestManualNeverExpires(t *testing.T) {
	l := NewLedger()
	past := time.Now().Add(-48 * time.Hour)
	l.Record(Entitlement{Provider: "p", Model: "", State: StateVerifiedPaid,
		Source: SourceManual, ObservedAt: past}) // 无 ValidUntil
	if e := l.Get("p", ""); e.State != StateVerifiedPaid {
		t.Fatalf("manual without valid_until must not expire, got %s", e.State)
	}
}
