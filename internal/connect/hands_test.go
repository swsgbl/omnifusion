package connect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFindToolNamedDiscovery 靶向发现：home 点目录与 AppData 变体
//（hm-harness 子串/宽松匹配）都能找到，且目录命中带顶层预览。
func TestFindToolNamedDiscovery(t *testing.T) {
	orig, err := homedir()
	if err != nil {
		t.Fatal(err)
	}
	fake := t.TempDir()
	t.Setenv("USERPROFILE", fake)
	t.Setenv("HOME", fake)
	defer os.Setenv("USERPROFILE", orig)
	_ = os.MkdirAll(filepath.Join(fake, ".hmharness"), 0o755)
	_ = os.WriteFile(filepath.Join(fake, ".hmharness", "config.json"), []byte(`{"a":1}`), 0o600)
	variant := filepath.Join(fake, "AppData", "Roaming", "HMHarness")
	_ = os.MkdirAll(variant, 0o755)
	_ = os.WriteFile(filepath.Join(variant, "settings.yaml"), []byte("mode: local\n"), 0o600)
	dashed := filepath.Join(fake, ".config", "hm-harness")
	_ = os.MkdirAll(dashed, 0o755)
	_ = os.WriteFile(filepath.Join(dashed, "conf.toml"), []byte("k = 1\n"), 0o600)

	hits := FindTool("hmharness")
	kinds := map[string]bool{}
	for _, h := range hits {
		if h.Kind == "dir" && len(h.Files) == 0 {
			t.Errorf("dir hit without preview: %s", h.Path)
		}
		kinds[h.Path] = true
	}
	for _, want := range []string{filepath.Join(fake, ".hmharness"), variant, dashed} {
		if !kinds[want] {
			t.Errorf("missed %s in %+v", want, hits)
		}
	}
}

// TestFindToolSweep 无名扫描：点目录按关键词过滤，噪声目录不出现。
func TestFindToolSweep(t *testing.T) {
	fake := t.TempDir()
	t.Setenv("USERPROFILE", fake)
	t.Setenv("HOME", fake)
	_ = os.MkdirAll(filepath.Join(fake, ".cursor"), 0o755)
	_ = os.MkdirAll(filepath.Join(fake, ".totally-unrelated"), 0o755)

	hits := FindTool("")
	joined := ""
	for _, h := range hits {
		joined += h.Path + ";"
	}
	if !strings.Contains(joined, ".cursor") {
		t.Errorf("sweep missed .cursor: %v", hits)
	}
	if strings.Contains(joined, ".totally-unrelated") {
		t.Errorf("sweep leaked noise dir: %v", hits)
	}
}

// TestResolveHomePathGuards 相对路径按 home 解析；".." 与越界绝对路径
// 一律拒绝。
func TestResolveHomePathGuards(t *testing.T) {
	home := t.TempDir()
	if p, err := resolveHomePath(home, ".foo/config.json"); err != nil || !underDir(home, p) {
		t.Errorf("relative resolve: %q %v", p, err)
	}
	if _, err := resolveHomePath(home, `..\..\.gitconfig`); err == nil {
		t.Error("dotdot escape accepted")
	}
	outside := filepath.Join(filepath.Dir(home), "elsewhere.json")
	if _, err := resolveHomePath(home, outside); err == nil {
		t.Error("outside-home absolute accepted")
	}
	if _, err := resolveHomePath(home, ""); err == nil {
		t.Error("empty path accepted")
	}
}

// TestReadConfigGuards 目录拒绝、二进制拒绝、内容原样返回。
func TestReadConfigGuards(t *testing.T) {
	fake := t.TempDir()
	t.Setenv("USERPROFILE", fake)
	t.Setenv("HOME", fake)
	sub := filepath.Join(fake, ".tool")
	_ = os.MkdirAll(sub, 0o755)
	cfg := filepath.Join(sub, "config.json")
	_ = os.WriteFile(cfg, []byte("{\"k\":\"v\"}"), 0o600)
	_ = os.WriteFile(filepath.Join(sub, "blob.bin"), []byte{0x00, 0x01, 0x02}, 0o600)

	r, err := ReadConfig(".tool/config.json")
	if err != nil || !strings.Contains(r["content"].(string), `"k"`) {
		t.Errorf("read config: %v %+v", err, r)
	}
	if _, err := ReadConfig(".tool"); err == nil {
		t.Error("directory read accepted")
	}
	if _, err := ReadConfig(".tool/blob.bin"); err == nil {
		t.Error("binary read accepted")
	}
	if _, err := ReadConfig("missing.json"); err == nil {
		t.Error("missing file accepted")
	}
}

// TestWriteConfigBackupAndNoMkdir 覆盖自动备份；目录不存在拒写
//（管家应回问用户，不是乱建目录）。
func TestWriteConfigBackupAndNoMkdir(t *testing.T) {
	fake := t.TempDir()
	t.Setenv("USERPROFILE", fake)
	t.Setenv("HOME", fake)
	dir := filepath.Join(fake, ".tool")
	_ = os.MkdirAll(dir, 0o755)
	cfg := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfg, []byte(`{"old":true}`), 0o600)

	res, err := WriteConfig(".tool/config.json", `{"old":true,"omnifusion":{"base":"http://127.0.0.1:20130/v1"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg + ".bak-ofd"); err != nil {
		t.Error("backup not created")
	}
	b, _ := os.ReadFile(cfg)
	if !strings.Contains(string(b), "omnifusion") {
		t.Errorf("content not written: %s", b)
	}
	if res["result"] == "" || res["path"] == "" {
		t.Errorf("result incomplete: %+v", res)
	}
	// 新文件（目录已存在）可建。
	if _, err := WriteConfig(".tool/new.json", "{}"); err != nil {
		t.Errorf("new file in existing dir refused: %v", err)
	}
	// 目录不存在：拒绝。
	if _, err := WriteConfig(".nonexistent/config.json", "{}"); err == nil {
		t.Error("write into missing dir accepted")
	}
	// 超限拒绝。
	if _, err := WriteConfig(".tool/big.json", strings.Repeat("x", writeCap+1)); err == nil {
		t.Error("oversized write accepted")
	}
	// 空内容拒绝（模型截断/参数丢失时绝不落盘清空用户配置），
	// 且原文件必须原样保留。
	if _, err := WriteConfig(".tool/config.json", ""); err == nil {
		t.Error("empty content accepted")
	}
	if _, err := WriteConfig(".tool/config.json", "   \n\t "); err == nil {
		t.Error("whitespace-only content accepted")
	}
	b2, _ := os.ReadFile(cfg)
	if !strings.Contains(string(b2), "omnifusion") {
		t.Error("file mutated by refused write")
	}
}

// TestPatchConfigDottedPaths 点补丁：只改目标键、其余字段一字不动、
// 缺失中间层自动创建、remove 删键、非 JSON/新文件拒绝、覆盖有备份。
func TestPatchConfigDottedPaths(t *testing.T) {
	fake := t.TempDir()
	t.Setenv("USERPROFILE", fake)
	t.Setenv("HOME", fake)
	dir := filepath.Join(fake, ".tool")
	_ = os.MkdirAll(dir, 0o755)
	cfg := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfg, []byte("{\"llm\":{\"apiBase\":\"https://old/v1\",\"apiKey\":\"sk-old\",\"model\":\"gpt-4o\",\"temperature\":0.7},\"keep\":{\"a\":1}}"), 0o600)

	res, err := PatchConfig(".tool/config.json", []PatchOp{
		{Path: "llm.apiBase", Value: "http://127.0.0.1:20130/v1"},
		{Path: "llm.apiKey", Value: "ofg-tok"},
		{Path: "llm.model", Value: "@quality"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res["applied"] != 3 {
		t.Errorf("applied = %v", res["applied"])
	}
	b, _ := os.ReadFile(cfg)
	s := string(b)
	for _, want := range []string{"http://127.0.0.1:20130/v1", "ofg-tok", "@quality", "\"temperature\": 0.7", "\"a\": 1"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if _, err := os.Stat(cfg + ".bak-ofd"); err != nil {
		t.Error("backup not created")
	}
	// 中间层自动创建 + remove 删键。
	if _, err := PatchConfig(".tool/config.json", []PatchOp{
		{Path: "omnifusion.note", Value: "wired"},
		{Path: "llm.temperature", Remove: true},
	}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(cfg)
	if !strings.Contains(string(b), "wired") || strings.Contains(string(b), "temperature") {
		t.Errorf("create/remove wrong:\n%s", b)
	}
	// 非 JSON 拒绝。
	_ = os.WriteFile(filepath.Join(dir, "conf.toml"), []byte("k = 1\n"), 0o600)
	if _, err := PatchConfig(".tool/conf.toml", []PatchOp{{Path: "a", Value: 1}}); err == nil {
		t.Error("non-JSON patch accepted")
	}
	// 不存在的文件拒绝（新文件走 write_config）。
	if _, err := PatchConfig(".tool/new.json", []PatchOp{{Path: "a", Value: 1}}); err == nil {
		t.Error("patch on missing file accepted")
	}
	// 空 ops 拒绝。
	if _, err := PatchConfig(".tool/config.json", nil); err == nil {
		t.Error("empty ops accepted")
	}
}

// homedir 取当前真实 home（测试恢复环境变量用）。
func homedir() (string, error) {
	return os.UserHomeDir()
}
