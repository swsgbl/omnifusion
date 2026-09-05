// connectcmd_tools.go 承载 Codex / Gemini CLI / OpenCode 三家的写入器。
// 定位规则逐家遵守其自有约定（CODEX_HOME、~/.gemini/.env、
// OPENCODE_CONFIG/XDG），写入前备份、--print 只打印。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// applyCodex 写 ~/.codex/config.toml：model_provider 指向 omnifusion
//（网关原生承接 Codex 的 wire_api="responses" 协议），密钥经
// OMNIFUSION_API_KEY 环境变量（Codex 自定义 provider 的官方口径）。
// 无 model 行时补 model = "@quality"（网关指令：自动选最强免费模型）。
func applyCodex(dir, base, token string, connect, printOnly bool) (string, error) {
	path := filepath.Join(dir, "config.toml")
	existing := ""
	if b, err := os.ReadFile(path); err == nil {
		existing = string(b)
	}
	if !connect {
		cleaned := removeCodexArtifacts(existing)
		if printOnly {
			return fmt.Sprintf("将从 %s 清除 omnifusion 配置:\n%s", path, cleaned), nil
		}
		if cleaned == existing {
			return fmt.Sprintf("%s 无 omnifusion 配置，无需清除", path), nil
		}
		bak := backupIfExists(path)
		if err := os.WriteFile(path, []byte(cleaned), 0o600); err != nil {
			return "", err
		}
		return fmt.Sprintf("已清除 %s%s", path, bakNote(bak)), nil
	}
	body := removeCodexArtifacts(existing)
	head := "model_provider = \"omnifusion\"\n"
	if !hasTOMLTopKey(body, "model") {
		head += "model = \"@quality\"\n"
	}
	block := "\n[model_providers.omnifusion]\nname = \"OmniFusion\"\nbase_url = \"" + base + "/v1\"\nwire_api = \"responses\"\nenv_key = \"OMNIFUSION_API_KEY\"\n"
	out := head + strings.TrimLeft(body, "\n") + block
	if printOnly {
		return fmt.Sprintf("将写入 %s:\n%s\n（另需环境变量 OMNIFUSION_API_KEY=%s）", path, out, token), nil
	}
	bak := backupIfExists(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		return "", err
	}
	envMsg, err := setPersistentEnv("OMNIFUSION_API_KEY", token)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("已写入 %s%s\n%s（重启 codex 生效）", path, bakNote(bak), envMsg), nil
}

// removeCodexArtifacts 清掉历史 connect 留下的块与键（不动用户其他配置）。
func removeCodexArtifacts(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	inBlock := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "[model_providers.omnifusion]") {
			inBlock = true
			continue
		}
		if inBlock {
			if strings.HasPrefix(t, "[") {
				inBlock = false
			} else {
				continue
			}
		}
		if strings.HasPrefix(t, "model_provider") && strings.Contains(t, "omnifusion") {
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

func hasTOMLTopKey(content, key string) bool {
	for _, ln := range strings.Split(content, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, key+" ") || strings.HasPrefix(t, key+"=") {
			return true
		}
	}
	return false
}

// setPersistentEnv 持久化用户级环境变量：Windows 走 setx（标准机制）；
// unix 追加到 ~/.profile 与 ~/.zshrc（已含则跳过）。
func setPersistentEnv(key, value string) (string, error) {
	if runtime.GOOS == "windows" {
		if out, err := exec.Command("setx", key, value).CombinedOutput(); err != nil {
			return "", fmt.Errorf("setx %s: %v: %s", key, err, strings.TrimSpace(string(out)))
		}
		return fmt.Sprintf("环境变量 %s 已写入用户级（setx；新开的终端生效）", key), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	var touched []string
	for _, rc := range []string{".profile", ".zshrc"} {
		p := filepath.Join(home, rc)
		b, err := os.ReadFile(p)
		line := "export " + key + "=\"" + value + "\"\n"
		if err == nil && strings.Contains(string(b), "export "+key+"=") {
			continue
		}
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			continue
		}
		_, _ = f.WriteString(line)
		_ = f.Close()
		touched = append(touched, p)
	}
	if len(touched) == 0 {
		return fmt.Sprintf("环境变量 %s 已存在，未改动", key), nil
	}
	return fmt.Sprintf("环境变量 %s 已追加至 %s（重开终端生效）", key, strings.Join(touched, ", ")), nil
}

// applyGemini 合并 ~/.gemini/.env（Gemini CLI 自动加载的 dotenv）：
// GOOGLE_GEMINI_BASE_URL 指向网关（Gemini 入站原生承接），令牌作
// GEMINI_API_KEY；若覆盖了原有值会在输出中明示（备份可回）。
func applyGemini(path, base, token string, connect, printOnly bool) (string, error) {
	const baseKey, tokenKey = "GOOGLE_GEMINI_BASE_URL", "GEMINI_API_KEY"
	raw := ""
	if b, err := os.ReadFile(path); err == nil {
		raw = string(b)
	}
	overridden := ""
	for _, ln := range strings.Split(raw, "\n") {
		t := strings.TrimSpace(ln)
		if connect && strings.HasPrefix(t, tokenKey+"=") && !strings.Contains(t, token) && t != tokenKey+"=" {
			overridden = "（注意：原 GEMINI_API_KEY 已被覆盖，原文件见备份）"
		}
	}
	var kept []string
	for _, ln := range strings.Split(raw, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, baseKey+"=") || strings.HasPrefix(t, tokenKey+"=") {
			continue
		}
		kept = append(kept, ln)
	}
	if connect {
		kept = append(kept, baseKey+"="+base, tokenKey+"="+token)
	}
	out := strings.TrimLeft(strings.Join(kept, "\n"), "\n")
	if printOnly {
		return fmt.Sprintf("将%s %s:\n%s", map[bool]string{true: "写入", false: "清理后保留"}[connect], path, out), nil
	}
	if !connect && out == strings.TrimLeft(raw, "\n") {
		return fmt.Sprintf("%s 无 omnifusion 接入痕迹，无需改动", path), nil
	}
	bak := backupIfExists(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(out+"\n"), 0o600); err != nil {
		return "", err
	}
	verb := map[bool]string{true: "已写入", false: "已清理"}[connect]
	return fmt.Sprintf("%s %s%s %s（重启 gemini 生效；部分模式如 --experimental-acp 不走该变量）", verb, path, bakNote(bak), overridden), nil
}

// applyOpenCode 合并 opencode.json 的 provider 块（官方 openai-compatible
// 口径）。JSONC 无法安全合并：检测到 .jsonc 时不写，给手动片段。
func applyOpenCode(home, base, token string, connect, printOnly bool) (string, error) {
	dir := ""
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		dir = filepath.Join(x, "opencode")
	} else {
		dir = filepath.Join(home, ".config", "opencode")
	}
	if p := os.Getenv("OPENCODE_CONFIG"); p != "" {
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			return "", fmt.Errorf("检测到 OPENCODE_CONFIG=%s（JSONC 不做自动合并）。手动片段:\n%s", p, opencodeSnippet(base, token, connect))
		}
	}
	path := filepath.Join(dir, "opencode.json")
	if _, err := os.Stat(filepath.Join(dir, "opencode.jsonc")); err == nil {
		return "", fmt.Errorf("检测到 %s（JSONC 不做自动合并）。手动片段:\n%s", filepath.Join(dir, "opencode.jsonc"), opencodeSnippet(base, token, connect))
	}
	m := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &m); err != nil {
			return "", fmt.Errorf("parse %s: %w（手动处理见 ofd connect opencode --print）", path, err)
		}
	}
	prov, _ := m["provider"].(map[string]any)
	if prov == nil {
		prov = map[string]any{}
	}
	if connect {
		prov["omnifusion"] = map[string]any{
			"npm":  "@ai-sdk/openai-compatible",
			"name": "OmniFusion",
			"options": map[string]any{
				"baseURL": base + "/v1",
				"apiKey":  token,
			},
			"models": map[string]any{
				"@quality": map[string]any{"name": "OmniFusion ⚡ auto (strongest free)"},
			},
		}
	} else {
		delete(prov, "omnifusion")
	}
	if len(prov) == 0 {
		delete(m, "provider")
	} else {
		m["provider"] = prov
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	if printOnly {
		return fmt.Sprintf("将%s %s:\n%s", map[bool]string{true: "写入", false: "从"}[connect], path, out), nil
	}
	bak := backupIfExists(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %s%s（重启 opencode 生效；模型选 OmniFusion ⚡ auto）", map[bool]string{true: "已写入", false: "已清除"}[connect], path, bakNote(bak)), nil
}

// applyPi 合并 ~/.pi/agent/models.json（pi coding agent 的自定义
// provider 文件）：providers.omnifusion = 聚合网关（OpenAI 兼容端点，
// 聚合令牌；模型暴露 @quality/@cheap 指令）——pi 从此用聚合密钥驱动，
// 厂商真实密钥不出网关。
func applyPi(path, base, token string, connect, printOnly bool) (string, error) {
	m := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &m); err != nil {
			return "", fmt.Errorf("parse %s: %w（手动处理见 ofd connect pi --print）", path, err)
		}
	}
	prov, _ := m["providers"].(map[string]any)
	if prov == nil {
		prov = map[string]any{}
	}
	if connect {
		prov["omnifusion"] = map[string]any{
			"baseUrl": base + "/v1",
			"api":     "openai-completions",
			"apiKey":  token,
			"models": []map[string]any{
				{"id": "@quality"},
				{"id": "@cheap"},
			},
		}
	} else {
		delete(prov, "omnifusion")
	}
	if len(prov) == 0 {
		delete(m, "providers")
	} else {
		m["providers"] = prov
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	if printOnly {
		return fmt.Sprintf("将%s %s:\n%s", map[bool]string{true: "写入", false: "从"}[connect], path, out), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	bak := backupIfExists(path)
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		return "", err
	}
	verb := map[bool]string{true: "已写入", false: "已清除"}[connect]
	tail := "（启动 pi 后 /model 选 omnifusion/@quality 即走聚合网关）"
	if !connect {
		tail = "（重启 pi 生效）"
	}
	return fmt.Sprintf("%s %s%s %s", verb, path, bakNote(bak), tail), nil
}

func opencodeSnippet(base, token string, connect bool) string {
	if !connect {
		return "{ ... 删除 provider.omnifusion 块 ... }"
	}
	b, _ := json.MarshalIndent(map[string]any{
		"provider": map[string]any{
			"omnifusion": map[string]any{
				"npm":  "@ai-sdk/openai-compatible",
				"name": "OmniFusion",
				"options": map[string]any{"baseURL": base + "/v1", "apiKey": token},
				"models": map[string]any{"@quality": map[string]any{"name": "OmniFusion ⚡ auto"}},
			},
		},
	}, "", "  ")
	return string(b)
}
