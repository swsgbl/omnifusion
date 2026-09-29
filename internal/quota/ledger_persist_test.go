package quota

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// mockPersister 记录 SaveEntitlement 调用（可选返回错误）。
type mockPersister struct {
	mu    sync.Mutex
	calls []Entitlement
	err   error
}

func (m *mockPersister) SaveEntitlement(e Entitlement) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, e)
	return m.err
}

func TestPersisterCalledOnEffectiveRecord(t *testing.T) {
	l := NewLedger()
	p := &mockPersister{}
	l.SetPersister(p)
	now := time.Now()
	l.Record(Entitlement{Provider: "p", State: StateVerifiedFree,
		Source: SourceRuntime429, ObservedAt: now})
	if len(p.calls) != 1 {
		t.Fatalf("persist calls = %d, want 1", len(p.calls))
	}
	if p.calls[0].Provider != "p" || p.calls[0].State != StateVerifiedFree {
		t.Errorf("persisted = %+v", p.calls[0])
	}
}

func TestPersisterNotCalledOnRejectedRecord(t *testing.T) {
	l := NewLedger()
	p := &mockPersister{}
	l.SetPersister(p)
	now := time.Now()
	// 先写高优先级运行时证据
	l.Record(Entitlement{Provider: "p", State: StateVerifiedPaid,
		Source: SourceRuntime429, ObservedAt: now})
	// 再写低优先级静态声明 → 被合并规则拒绝 → 不落库
	l.Record(Entitlement{Provider: "p", State: StateVerifiedFree,
		Source: SourceStaticCatalog, ObservedAt: now})
	if len(p.calls) != 1 {
		t.Fatalf("persist calls = %d, want 1 (rejected evidence must not overwrite store)", len(p.calls))
	}
	if p.calls[0].State != StateVerifiedPaid {
		t.Errorf("persisted state = %s, want VERIFIED_PAID (first call)", p.calls[0].State)
	}
}

func TestPersisterErrorDoesNotBlockMemory(t *testing.T) {
	l := NewLedger()
	p := &mockPersister{err: errors.New("disk full")}
	l.SetPersister(p)
	l.Record(Entitlement{Provider: "p", State: StateVerifiedFree,
		Source: SourceRuntime429, ObservedAt: time.Now()})
	// 内存路径不受影响
	if e := l.Get("p", ""); e.State != StateVerifiedFree {
		t.Fatalf("memory state = %s, want VERIFIED_FREE (persist failure must not block)", e.State)
	}
}

func TestLoadFromMergesWithPriority(t *testing.T) {
	l := NewLedger()
	now := time.Now()
	// 恢复：静态 free（持久化里的旧事实）
	l.LoadFrom([]Entitlement{
		{Provider: "p", State: StateVerifiedFree, Source: SourceStaticCatalog, ObservedAt: now},
	})
	// 新运行时证据覆盖
	l.Record(Entitlement{Provider: "p", State: StateVerifiedPaid,
		Source: SourceRuntime429, ObservedAt: now})
	if e := l.Get("p", ""); e.State != StateVerifiedPaid {
		t.Fatalf("state = %s, want VERIFIED_PAID (runtime overrides restored static)", e.State)
	}
}

func TestNilPersisterIsNoOp(t *testing.T) {
	l := NewLedger()
	l.SetPersister(nil) // nil 安全
	l.Record(Entitlement{Provider: "p", State: StateUnknown, Source: SourceStaticCatalog, ObservedAt: time.Now()})
	if e := l.Get("p", ""); e.State != StateUnknown {
		t.Errorf("state = %s", e.State)
	}
}
