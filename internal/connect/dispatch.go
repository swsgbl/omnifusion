// dispatch.go 是 connect 包的统一入口：Wire/Unwire/Scan。命令行
// （ofd connect）与 dashboard 管家 API 共用这一份实现。
package connect

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Wire 把聚合网关（base 为根地址、token 为聚合令牌）确定性接入目标
// CLI 的标准配置，写前自动备份。返回人类可读的结果消息。
func Wire(target, base, token string) (string, error) {
	return apply(target, base, token, true, false)
}

// Unwire 从目标 CLI 的配置中移除 omnifusion 接入（原路清除）。
func Unwire(target string) (string, error) {
	return apply(target, "", "", false, false)
}

// Plan 返回将执行/清除的配置内容预览（不落盘；ofd connect --print 用）。
func Plan(target, base, token string, unwire bool) (string, error) {
	return apply(target, base, token, !unwire, true)
}

func apply(target, base, token string, connect, printOnly bool) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	switch target {
	case "claude":
		return applyClaude(filepath.Join(claudeDir(home), "settings.json"), base, token, connect, printOnly)
	case "codex":
		return applyCodex(codexDir(home), base, token, connect, printOnly)
	case "gemini":
		return applyGemini(filepath.Join(home, ".gemini", ".env"), base, token, connect, printOnly)
	case "opencode":
		return applyOpenCode(home, base, token, connect, printOnly)
	case "pi":
		return applyPi(filepath.Join(home, ".pi", "agent", "models.json"), base, token, connect, printOnly)
	default:
		return "", fmt.Errorf("connect: unknown target %q", target)
	}
}

// ToolInfo 是 scan 报告的一行：一个已知 CLI 的本机状态。
type ToolInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Installed  bool   `json:"installed"`   // 配置目录存在
	Connected  bool   `json:"connected"`   // 聚合网关已接入
	ConfigPath string `json:"config_path"` // 配置文件路径
}

// Scan 检测本机已知 AI CLI 的安装与接入状态（只查已知配置目录，
// 不做任意文件系统访问）。origin 是网关根地址（用于识别"已接入"）。
func Scan(origin string) []ToolInfo {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	type loc struct {
		dirFn   func(home string) string
		file    string
		markers []string // 配置文件中出现任一标记 = 已接入
	}
	locs := map[string]loc{
		"claude": {dirFn: claudeDir, file: "settings.json",
			markers: []string{"omnifusion", origin}},
		"codex": {dirFn: codexDir, file: "config.toml",
			markers: []string{"model_providers.omnifusion"}},
		"gemini": {dirFn: func(string) string { return filepath.Join(home, ".gemini") }, file: ".env",
			markers: []string{origin}},
		"opencode": {dirFn: func(string) string { return filepath.Join(home, ".config", "opencode") }, file: "opencode.json",
			markers: []string{"omnifusion"}},
		"pi": {dirFn: func(string) string { return filepath.Join(home, ".pi", "agent") }, file: "models.json",
			markers: []string{"omnifusion"}},
	}
	out := make([]ToolInfo, 0, len(Targets))
	for _, t := range Targets {
		l := locs[t.ID]
		dir := l.dirFn(home)
		cfgPath := filepath.Join(dir, l.file)
		info := ToolInfo{ID: t.ID, Name: t.Name, ConfigPath: cfgPath}
		if _, err := os.Stat(dir); err == nil {
			info.Installed = true
		}
		if b, err := os.ReadFile(cfgPath); err == nil {
			for _, mk := range l.markers {
				if mk != "" && strings.Contains(string(b), mk) {
					info.Connected = true
					break
				}
			}
		}
		out = append(out, info)
	}
	return out
}
