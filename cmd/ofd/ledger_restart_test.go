package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/config"
	"github.com/swsgbl/omnifusion/internal/quota"
	"github.com/swsgbl/omnifusion/internal/security"
	"github.com/swsgbl/omnifusion/internal/store"
)

// TestLedgerSurvivesRestart 蓝图核心场景：运行时 429 观测 → 落库 →
// 重启（重新 buildRouter）→ 权益事实恢复（不被静态种子降级覆盖）。
func TestLedgerSurvivesRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "restart.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	kr, err := security.Open("")
	if err != nil {
		t.Fatalf("open keyring: %v", err)
	}
	t.Setenv("CEREBRAS_API_KEY", "k")

	// 第一世：装配 → 运行时 402 观测（付费证据，优先级高于静态种子）
	r1, _ := buildRouter(&config.Config{}, discardLog(), st, kr)
	r1.Ledger.Record(quota.Entitlement{
		Provider: "cerebras", State: quota.StateVerifiedPaid,
		Source: quota.SourceRuntime429, Window: quota.QuotaWindow{Remaining: -1},
		ObservedAt: time.Now(), ValidUntil: time.Now().Add(24 * time.Hour),
		EvidenceID: "paid:restart-test",
	})
	if e := r1.Ledger.Get("cerebras", ""); e.State != quota.StateVerifiedPaid {
		t.Fatalf("first life state = %s, want VERIFIED_PAID", e.State)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// 第二世：同一个库重新装配 → 恢复必须带回 VERIFIED_PAID
	st2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st2.Close()
	r2, _ := buildRouter(&config.Config{}, discardLog(), st2, kr)
	e := r2.Ledger.Get("cerebras", "")
	if e.State != quota.StateVerifiedPaid {
		t.Fatalf("second life state = %s, want VERIFIED_PAID (restored, not downgraded by static seed)", e.State)
	}
	if e.Source != quota.SourceRuntime429 {
		t.Errorf("second life source = %s, want runtime_429", e.Source)
	}
	if e.EvidenceID != "paid:restart-test" {
		t.Errorf("evidence id = %s, want paid:restart-test", e.EvidenceID)
	}
}
