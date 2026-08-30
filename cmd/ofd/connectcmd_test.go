package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/config"
)

// connectTestCfg 返回固定端口的配置（base URL 断言用）。
func connectTestCfg() *config.Config {
	cfg := config.Default()
	cfg.Server.Port = 20130
	return cfg
}

// TestConnectClaudeMergeAndDisconnect 走 applyClaude 全链路：合并保留
// 已有 env 键、备份生成、disconnect 精确移除我们写的两个键。
func TestConnectClaudeMergeAndDisconnect(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"env":{"EXISTING":"keep"},"permissions":{"allow":["Bash"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := applyClaude(path, "http://127.0.0.1:20130", "ofg-tok", true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".bak-ofd"); err != nil {
		t.Error("backup not created")
	}
	b, _ := os.ReadFile(path)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("output not json: %v", err)
	}
	env := m["env"].(map[string]any)
	if env["EXISTING"] != "keep" || env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:20130" || env["ANTHROPIC_AUTH_TOKEN"] != "ofg-tok" {
		t.Errorf("merge wrong: %v", env)
	}
	if m["permissions"] == nil {
		t.Error("existing non-env keys lost")
	}
	if _, err := applyClaude(path, "http://127.0.0.1:20130", "ofg-tok", false, false); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	_ = json.Unmarshal(b, &m)
	env = m["env"].(map[string]any)
	if _, ok := env["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Error("disconnect left our token")
	}
	if env["EXISTING"] != "keep" {
		t.Error("disconnect removed user keys")
	}
}

// TestConnectCodexTopKeyBeforeTables TOML 顶层键必须在表头之前；
// 二次 connect 幂等（不重复堆块）。
func TestConnectCodexTopKeyBeforeTables(t *testing.T) {
	dir := t.TempDir()
	if _, err := applyCodex(dir, "http://127.0.0.1:20130", "ofg-tok", true, false); err != nil {
		t.Fatal(err)
	}
	// 用户已有配置（含表头）：二次写入不得把顶层键塞进表后。
	if err := os.WriteFile(filepath.Join(dir, "config.toml"),
		[]byte("model = \"gpt-5\"\n\n[other]\nx = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := applyCodex(dir, "http://127.0.0.1:20130", "ofg-tok", true, false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	s := string(b)
	idxProvider := strings.Index(s, "model_provider = \"omnifusion\"")
	idxTable := strings.Index(s, "[other]")
	if idxProvider == -1 || idxTable == -1 || idxProvider > idxTable {
		t.Fatalf("model_provider must precede tables:\n%s", s)
	}
	if !strings.Contains(s, "[model_providers.omnifusion]") || !strings.Contains(s, "wire_api = \"responses\"") {
		t.Errorf("provider block missing:\n%s", s)
	}
	if strings.Count(s, "[model_providers.omnifusion]") != 1 {
		t.Error("duplicate omnifusion block (idempotency broken)")
	}
	if !hasTOMLTopKey(s, "model") && !strings.Contains(s, "model = \"@quality\"") {
		t.Error("model line missing")
	}
	// disconnect 清干净。
	if _, err := applyCodex(dir, "http://127.0.0.1:20130", "ofg-tok", false, false); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "config.toml"))
	if strings.Contains(string(b), "omnifusion") {
		t.Errorf("disconnect left artifacts:\n%s", b)
	}
}

// TestConnectGeminiOverrideNotice 覆盖既有 GEMINI_API_KEY 时必须给出
// 提示；--print 不落盘。
func TestConnectGeminiOverrideNotice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("GEMINI_API_KEY=user-google-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	msg, err := applyGemini(path, "http://127.0.0.1:20130", "ofg-tok", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "覆盖") {
		t.Errorf("override notice missing: %s", msg)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:20130") {
		t.Errorf("base url not written:\n%s", b)
	}
	// --print 不落盘。
	path2 := filepath.Join(dir, "print.env")
	msg, err = applyGemini(path2, "http://127.0.0.1:20130", "ofg-tok", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "GEMINI_API_KEY=ofg-tok") {
		t.Errorf("print output missing token line: %s", msg)
	}
	if _, err := os.Stat(path2); !os.IsNotExist(err) {
		t.Error("--print must not write files")
	}
}

// TestConnectOpenCodeMergeAndJSONCGuard opencode.json 合并 + .jsonc 存在
// 时拒绝自动写、给手动片段。
func TestConnectOpenCodeMergeAndJSONCGuard(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"),
		[]byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := applyOpenCode(home, "http://127.0.0.1:20130", "ofg-tok", true, false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "opencode.json"))
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["theme"] != "dark" {
		t.Error("existing keys lost")
	}
	prov := m["provider"].(map[string]any)["omnifusion"].(map[string]any)
	if prov["npm"] != "@ai-sdk/openai-compatible" {
		t.Errorf("provider block wrong: %v", prov)
	}
	// jsonc 守卫：存在 .jsonc 时报错并附手动片段。
	if err := os.WriteFile(filepath.Join(dir, "opencode.jsonc"), []byte("// jsonc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := applyOpenCode(home, "http://127.0.0.1:20130", "ofg-tok", true, false)
	if err == nil || !strings.Contains(err.Error(), "手动片段") {
		t.Errorf("jsonc guard missing: %v", err)
	}
}

// TestClientBaseURLWildcard 归一化：通配/空监听地址对客户端回环。
func TestClientBaseURLWildcard(t *testing.T) {
	cfg := connectTestCfg()
	cfg.Server.Host = "0.0.0.0"
	if got := clientBaseURL(cfg); got != "http://127.0.0.1:20130" {
		t.Errorf("base = %q", got)
	}
}
