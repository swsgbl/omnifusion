package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/config"
	"github.com/swsgbl/omnifusion/internal/provider/registry"
	"github.com/swsgbl/omnifusion/internal/store"
)

func statusEntries(t *testing.T) []registry.Entry {
	t.Helper()
	entries, err := registry.Load()
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	return entries
}

// TestBuildProviderStatusesLayers 是 M2.6 验收（三层视图）：
// provider 层（ready/no-key/missing var）、key 层（stored/env/none）、
// model 层（活跃锁定）各归其位。
func TestBuildProviderStatusesLayers(t *testing.T) {
	entries := statusEntries(t)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	env := func(name string) string {
		if name == "GROQ_API_KEY" {
			return "gsk-x" // CLOUDFLARE_ACCOUNT_ID 故意不给：测 missing 变量分支
		}
		return ""
	}
	conns := map[string]store.Connection{
		"openrouter": {Provider: "openrouter", Label: "main"},
		"cloudflare": {Provider: "cloudflare"}, // 有 key 但缺 account_id
	}
	cds := []store.Cooldown{
		{ScopeType: "connection", Provider: "groq", Until: now.Add(20 * time.Minute), Reason: "rate_limit"},
		{ScopeType: "model", Provider: "groq", Model: "llama-scout", Until: now.Add(time.Hour), Reason: "quota_exhausted"},
	}

	stats := buildProviderStatuses(entries, conns, env, cds, now)
	byID := map[string]providerStatus{}
	for _, ps := range stats {
		byID[ps.ID] = ps
	}
	if len(stats) != len(entries) {
		t.Fatalf("got %d rows, want %d", len(stats), len(entries))
	}

	// key 层三种来源 + label。
	if ps := byID["groq"]; ps.Key != "env:GROQ_API_KEY" || ps.State != "cooldown" {
		t.Errorf("groq = %+v, want env key + cooldown state", ps)
	}
	if ps := byID["openrouter"]; ps.Key != "stored(main)" || ps.State != "ready" || !ps.Ready {
		t.Errorf("openrouter = %+v, want stored(main) + ready", ps)
	}
	// model 层：groq 同时挂 connection 冷却与 model 锁定。
	if !strings.Contains(byID["groq"].Isolation, "cooldown 20m0s (rate_limit)") {
		t.Errorf("groq isolation = %q, want connection cooldown", byID["groq"].Isolation)
	}
	if !strings.Contains(byID["groq"].Isolation, "model llama-scout locked 1h0m0s (quota_exhausted)") {
		t.Errorf("groq isolation = %q, want model lockout", byID["groq"].Isolation)
	}
	// provider 层：缺 URL 变量的半配置态。
	if ps := byID["cloudflare"]; ps.State != "missing account_id" {
		t.Errorf("cloudflare = %+v, want missing account_id", ps)
	}
	// ollama 无 key 也 ready。
	if ps := byID["ollama"]; ps.Key != "-" || ps.State != "ready" || !ps.Ready {
		t.Errorf("ollama = %+v, want optional key + ready", ps)
	}
	// 未配 key 的 gated provider。
	if ps := byID["gemini"]; ps.Key != "none" || ps.State != "no-key" || ps.Ready {
		t.Errorf("gemini = %+v, want none/no-key", ps)
	}
	// 免费层事实进 QUOTA 列。
	if ps := byID["groq"]; !strings.Contains(ps.Quota, "rpm=30") || !strings.Contains(ps.Quota, "rpd=14400") {
		t.Errorf("groq quota = %q, want rpm=30 rpd=14400", ps.Quota)
	}
	if ps := byID["gemini"]; ps.Quota != "-" {
		t.Errorf("gemini quota = %q, want -", ps.Quota)
	}
}

// TestRunStatusAgainstStore 走真实 SQLite：连接与冷却落库后，
// ofd status 的完整渲染包含三层信息。
func TestRunStatusAgainstStore(t *testing.T) {
	cfg := &config.Config{}
	cfg.Store.Path = filepath.Join(t.TempDir(), "status.db")

	st, err := store.Open(cfg.Store.Path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	now := time.Now()
	if err := st.SetConnection("groq", []byte("enc"), "main"); err != nil {
		t.Fatalf("SetConnection: %v", err)
	}
	if err := st.UpsertCooldown(store.Cooldown{
		ScopeType: "model", Provider: "groq", Model: "llama-scout",
		Until: now.Add(30 * time.Minute), Reason: "quota_exhausted",
	}); err != nil {
		t.Fatalf("UpsertCooldown: %v", err)
	}
	st.Close()

	if err := runStatusCommand(cfg); err != nil { // 输出到 stdout（人工可见）
		t.Fatalf("runStatusCommand: %v", err)
	}

	// 复用同一数据源做渲染断言（runStatusCommand 写 os.Stdout，改走 render）。
	st2, err := store.Open(cfg.Store.Path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	conns := map[string]store.Connection{}
	for _, c := range mustListConnections(st2) {
		conns[c.Provider] = c
	}
	cds := mustLoadCooldowns(st2, now)
	stats := buildProviderStatuses(statusEntries(t), conns, func(string) string { return "" }, cds, now)

	var buf bytes.Buffer
	renderStatus(&buf, stats, cfg.Store.Path)
	for _, want := range []string{
		"groq", "stored(main)", "ready",
		"rpm=30 rpd=14400",
		"model llama-scout locked",
		"(quota_exhausted)",
		"gemini", "no-key",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("rendered status missing %q\n%s", want, buf.String())
		}
	}
	if !strings.Contains(buf.String(), "1/9 providers ready") && !strings.Contains(buf.String(), "2/9 providers ready") {
		// ollama（免 key）必然 ready；groq 有 key 也 ready —— 至少 2 家。
		t.Errorf("ready count line unexpected:\n%s", buf.String())
	}
}
