package store

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st, path
}

func TestCooldownRoundTrip(t *testing.T) {
	st, _ := newTestStore(t)
	now := time.Now()
	until := now.Add(10 * time.Minute)

	if err := st.UpsertCooldown(Cooldown{
		ScopeType: "connection", Provider: "groq", Until: until, Reason: "rate_limit",
	}); err != nil {
		t.Fatalf("UpsertCooldown: %v", err)
	}
	if err := st.UpsertCooldown(Cooldown{
		ScopeType: "model", Provider: "groq", Model: "llama-x", Until: until.Add(time.Hour), Reason: "quota_exhausted",
	}); err != nil {
		t.Fatalf("UpsertCooldown(model): %v", err)
	}

	rows, err := st.LoadCooldowns(now)
	if err != nil {
		t.Fatalf("LoadCooldowns: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(rows), rows)
	}
	byScope := map[string]Cooldown{}
	for _, c := range rows {
		byScope[c.ScopeType] = c
	}
	if c := byScope["connection"]; c.Provider != "groq" || c.Reason != "rate_limit" {
		t.Errorf("connection row = %+v", c)
	}
	if c := byScope["model"]; c.Model != "llama-x" || c.Reason != "quota_exhausted" {
		t.Errorf("model row = %+v", c)
	}
}

func TestCooldownUpsertNeverShortens(t *testing.T) {
	st, _ := newTestStore(t)
	now := time.Now()
	long := now.Add(30 * time.Minute)
	short := now.Add(1 * time.Minute)

	if err := st.UpsertCooldown(Cooldown{ScopeType: "connection", Provider: "p", Until: long, Reason: "a"}); err != nil {
		t.Fatal(err)
	}
	// 短冷却不得缩短已生效的长冷却
	if err := st.UpsertCooldown(Cooldown{ScopeType: "connection", Provider: "p", Until: short, Reason: "b"}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.LoadCooldowns(now.Add(2 * time.Minute)) // short 已过期
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].Until.After(now.Add(29*time.Minute)) {
		t.Errorf("rows = %+v, want only the long cooldown", rows)
	}
	// 长冷却之后可被更晚的覆盖（Unix 秒粒度比较：存储层截断纳秒）
	later := now.Add(time.Hour)
	if err := st.UpsertCooldown(Cooldown{ScopeType: "connection", Provider: "p", Until: later, Reason: "c"}); err != nil {
		t.Fatal(err)
	}
	rows, _ = st.LoadCooldowns(now.Add(31 * time.Minute))
	if len(rows) != 1 || rows[0].Until.Unix() != later.Unix() {
		t.Errorf("extend rows = %+v", rows)
	}
}

func TestLoadCooldownsFiltersExpired(t *testing.T) {
	st, _ := newTestStore(t)
	now := time.Now()
	if err := st.UpsertCooldown(Cooldown{ScopeType: "connection", Provider: "dead", Until: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertCooldown(Cooldown{ScopeType: "connection", Provider: "live", Until: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.LoadCooldowns(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Provider != "live" {
		t.Errorf("rows = %+v, want only live", rows)
	}
	if err := st.ClearExpiredCooldowns(now); err != nil {
		t.Fatal(err)
	}
	// 清理只删 dead；live 未到期仍在
	rows, err = st.LoadCooldowns(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Provider != "live" {
		t.Errorf("rows = %+v, want live to survive the cleanup", rows)
	}
}
