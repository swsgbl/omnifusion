package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/quota"
)

// 权益账本持久化契约测试：对象 → 行 → 库 → 行 → 对象 全链路往返不丢失。

func newEntStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "ent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestEntitlementRoundTrip(t *testing.T) {
	st := newEntStore(t)
	observed := time.Date(2026, 9, 25, 14, 30, 0, 0, time.UTC)
	valid := observed.Add(time.Hour)
	e := quota.Entitlement{
		Provider: "groq", Model: "",
		State: quota.StateVerifiedFree, Source: quota.SourceRuntime429,
		Window: quota.QuotaWindow{
			RPM: 30, RPD: 14400, TPM: 0, TPD: 0, Remaining: 0.73,
		},
		EvidenceID: "429:2026-09-25T14:30:00Z",
		TermsURL:   "https://groq.com/terms",
		ObservedAt: observed, ValidUntil: valid,
		Confidence: 0.9, CatalogVersion: "registry",
	}
	if err := st.SaveEntitlement(e); err != nil {
		t.Fatalf("SaveEntitlement: %v", err)
	}

	got, err := st.RestoreEntitlements()
	if err != nil {
		t.Fatalf("RestoreEntitlements: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("restored %d entries, want 1", len(got))
	}
	r := got[0]
	if r.Provider != "groq" || r.State != quota.StateVerifiedFree ||
		r.Source != quota.SourceRuntime429 {
		t.Errorf("scalars = %+v", r)
	}
	if r.Window.RPM != 30 || r.Window.RPD != 14400 || r.Window.Remaining != 0.73 {
		t.Errorf("window = %+v", r.Window)
	}
	if r.EvidenceID != e.EvidenceID || r.TermsURL != e.TermsURL ||
		r.Confidence != 0.9 || r.CatalogVersion != "registry" {
		t.Errorf("evidence fields = %+v", r)
	}
	if !r.ObservedAt.Equal(observed) || !r.ValidUntil.Equal(valid) {
		t.Errorf("timestamps: observed %v (want %v), valid %v (want %v)",
			r.ObservedAt, observed, r.ValidUntil, valid)
	}
}

func TestEntitlementRemainingUnknownRoundTrip(t *testing.T) {
	st := newEntStore(t)
	// remaining = -1（未知哨兵）→ SQL NULL → 恢复回 -1
	e := quota.Entitlement{
		Provider: "p", Model: "m",
		State: quota.StateVerifiedFree, Source: quota.SourceStaticCatalog,
		Window:     quota.QuotaWindow{RPM: 5, Remaining: -1},
		ObservedAt: time.Now(),
	}
	if err := st.SaveEntitlement(e); err != nil {
		t.Fatalf("SaveEntitlement: %v", err)
	}
	got, err := st.RestoreEntitlements()
	if err != nil || len(got) != 1 {
		t.Fatalf("restore: %v len=%d", err, len(got))
	}
	if got[0].Window.Remaining != -1 {
		t.Errorf("remaining = %v, want -1 (NULL ↔ unknown sentinel)", got[0].Window.Remaining)
	}
}

func TestEntitlementUpsertOverwrites(t *testing.T) {
	st := newEntStore(t)
	save := func(state quota.EntitlementState, source quota.Source) {
		e := quota.Entitlement{
			Provider: "p", Model: "", State: state, Source: source,
			Window: quota.QuotaWindow{Remaining: -1}, ObservedAt: time.Now(),
		}
		if err := st.SaveEntitlement(e); err != nil {
			t.Fatalf("SaveEntitlement(%s): %v", state, err)
		}
	}
	save(quota.StateVerifiedFree, quota.SourceStaticCatalog)
	save(quota.StateVerifiedPaid, quota.SourceRuntime429)
	got, err := st.RestoreEntitlements()
	if err != nil || len(got) != 1 {
		t.Fatalf("restore: %v len=%d (want 1 row — upsert replaces)", err, len(got))
	}
	if got[0].State != quota.StateVerifiedPaid || got[0].Source != quota.SourceRuntime429 {
		t.Errorf("upsert did not replace: %+v", got[0])
	}
}

func TestEntitlementEmptyModelAndZeroTime(t *testing.T) {
	st := newEntStore(t)
	// 空 model + 零时间（永久事实）必须可往返
	e := quota.Entitlement{
		Provider: "q", Model: "",
		State: quota.StateDisabled, Source: quota.SourceManual,
		Window: quota.QuotaWindow{Remaining: -1},
	}
	if err := st.SaveEntitlement(e); err != nil {
		t.Fatalf("SaveEntitlement: %v", err)
	}
	got, _ := st.RestoreEntitlements()
	if len(got) != 1 || got[0].Model != "" || !got[0].ValidUntil.IsZero() {
		t.Errorf("zero-value round trip: %+v", got)
	}
}
