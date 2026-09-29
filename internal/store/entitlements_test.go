// 权益账本持久化契约测试：行 → 库 → 行 全链路往返不丢失。
// quota.Entitlement ↔ Row 的转换在装配层（cmd/ofd）——这里测纯行语义，
// 状态/来源用裸字符串字面量（infra-store 的 depguard 管所有文件，
// 测试也不能 import quota）。
package store

import (
	"path/filepath"
	"testing"
	"time"
)

func newEntStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "ent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func zeroTime() time.Time    { return time.Time{} }
func rem(v float64) *float64 { return &v }

func TestEntitlementRoundTrip(t *testing.T) {
	st := newEntStore(t)
	r := EntitlementRow{
		Provider: "groq", State: "VERIFIED_FREE",
		Source: "runtime_429",
		RPM:    30, RPD: 14400, Remaining: rem(0.73),
		EvidenceID: "429:2026-09-25T14:30:00Z",
		TermsURL:   "https://groq.com/terms",
		ObservedAt: "2026-09-25T14:30:00Z", ValidUntil: "2026-09-25T15:30:00Z",
		Confidence: 0.9, CatalogVersion: "registry",
	}
	if err := st.UpsertEntitlement(r); err != nil {
		t.Fatalf("UpsertEntitlement: %v", err)
	}
	got, err := st.LoadEntitlements()
	if err != nil || len(got) != 1 {
		t.Fatalf("load: %v len=%d", err, len(got))
	}
	g := got[0]
	if g.Provider != "groq" || g.State != "VERIFIED_FREE" ||
		g.Source != "runtime_429" || g.RPM != 30 || g.RPD != 14400 {
		t.Errorf("scalars = %+v", g)
	}
	if g.Remaining == nil || *g.Remaining != 0.73 {
		t.Errorf("remaining = %v, want 0.73", g.Remaining)
	}
	if g.EvidenceID != r.EvidenceID || g.TermsURL != r.TermsURL ||
		g.Confidence != 0.9 || g.CatalogVersion != "registry" {
		t.Errorf("evidence fields = %+v", g)
	}
	if g.ObservedAt != r.ObservedAt || g.ValidUntil != r.ValidUntil {
		t.Errorf("timestamps = %+v", g)
	}
}

func TestEntitlementRemainingNullRoundTrip(t *testing.T) {
	st := newEntStore(t)
	if err := st.UpsertEntitlement(EntitlementRow{
		Provider: "p", State: "VERIFIED_FREE",
		Source: "static_catalog", RPM: 5,
		Remaining: nil, // 未知 → SQL NULL
	}); err != nil {
		t.Fatalf("UpsertEntitlement: %v", err)
	}
	got, _ := st.LoadEntitlements()
	if len(got) != 1 || got[0].Remaining != nil {
		t.Errorf("remaining = %v, want nil (NULL ↔ unknown)", got[0].Remaining)
	}
}

func TestEntitlementUpsertOverwrites(t *testing.T) {
	st := newEntStore(t)
	up := func(state, source string) {
		if err := st.UpsertEntitlement(EntitlementRow{
			Provider: "p", State: state, Source: source,
		}); err != nil {
			t.Fatalf("UpsertEntitlement(%s): %v", state, err)
		}
	}
	up("VERIFIED_FREE", "static_catalog")
	up("VERIFIED_PAID", "runtime_429")
	got, _ := st.LoadEntitlements()
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1 (upsert replaces)", len(got))
	}
	if got[0].State != "VERIFIED_PAID" {
		t.Errorf("upsert did not replace: %+v", got[0])
	}
}

func TestTimeHelpersRoundTrip(t *testing.T) {
	if FormatRFC3339(zeroTime()) != "" {
		t.Error("zero time must format to empty string")
	}
	if !ParseRFC3339("").IsZero() || !ParseRFC3339("garbage").IsZero() {
		t.Error("empty/bad strings must parse to zero time")
	}
	// 正常往返
	ts := ParseRFC3339("2026-09-25T14:30:00Z")
	if ts.IsZero() {
		t.Fatal("valid RFC3339 must parse")
	}
	if FormatRFC3339(ts) != "2026-09-25T14:30:00Z" {
		t.Errorf("format = %s", FormatRFC3339(ts))
	}
}
