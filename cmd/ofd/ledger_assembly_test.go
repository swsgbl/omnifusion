package main

import (
	"path/filepath"
	"testing"

	"github.com/swsgbl/omnifusion/internal/config"
	"github.com/swsgbl/omnifusion/internal/quota"
	"github.com/swsgbl/omnifusion/internal/security"
	"github.com/swsgbl/omnifusion/internal/store"
)

// TestBuildRouterAssemblesLedger 验证生产装配把权益账本接进 Router
// （蓝图 Phase 3 的最后一环：账本从测试专属变为生产运行时）。
// 同时验证注册表静态配额声明作为初始证据喂入（Source=static_catalog）。
func TestBuildRouterAssemblesLedger(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	kr, err := security.Open("")
	if err != nil {
		t.Fatalf("open keyring: %v", err)
	}
	// cerebras 内置声明 rpm/tpm/tpd → 应作为静态证据进账本
	t.Setenv("CEREBRAS_API_KEY", "k")
	// deepseek 无任何 rate_limits 声明 → 保持 UNKNOWN
	t.Setenv("DEEPSEEK_API_KEY", "k")

	r, _ := buildRouter(&config.Config{}, discardLog(), st, kr)
	if r.Ledger == nil {
		t.Fatal("production Router must have a Ledger wired")
	}
	e := r.Ledger.Get("cerebras", "")
	if e.State != quota.StateVerifiedFree {
		t.Fatalf("cerebras state = %s, want VERIFIED_FREE (static catalog declares free tier)", e.State)
	}
	if e.Source != quota.SourceStaticCatalog {
		t.Fatalf("source = %s, want static_catalog", e.Source)
	}
	if e.Window.TPD != 1000000 {
		t.Errorf("static TPD cap = %d, want 1000000 (cerebras declares tpd)", e.Window.TPD)
	}
	if e.Window.Remaining != -1 {
		t.Errorf("static declaration remaining = %v, want -1 (no headroom concept for static)", e.Window.Remaining)
	}
	// 无静态声明的家保持 UNKNOWN（不确定不猜——deepseek 不声明 rate_limits）
	if u := r.Ledger.Get("deepseek", ""); u.State != quota.StateUnknown {
		t.Errorf("deepseek without declaration = %s, want UNKNOWN", u.State)
	}
}

// TestBuildRouterEmptyRegistryStillHasLedger 空注册表（无凭据环境）也带账本
// ——装配无条件，行为一致。
func TestBuildRouterEmptyRegistryStillHasLedger(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	kr, err := security.Open("")
	if err != nil {
		t.Fatalf("open keyring: %v", err)
	}
	r, _ := buildRouter(&config.Config{}, discardLog(), st, kr)
	if r.Ledger == nil {
		t.Fatal("empty-registry Router must still carry a Ledger")
	}
}
