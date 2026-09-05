// Package connect 把 OmniFusion 聚合网关确定性接入本机已装的 AI 编码
// CLI（Claude Code / Codex / Gemini CLI / OpenCode / pi）：按各家官方
// 配置格式写入聚合地址与令牌，写前自动备份。供 ofd connect 命令与
// dashboard 管家 API 共用——同一份实现，行为完全一致。
//
// 安全边界：只写已知工具的已知配置文件（备份先行）；扫描只查已知
// 配置目录与 PATH，不做任意文件系统访问。
package connect

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Origin 归一化客户端应指向的网关根地址：监听通配/空地址时客户端走
// 回环，端口取当前配置真值。CLI 与管家 API 共用，保证写入的目标一致。
func Origin(host string, port int) string {
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}

// Target 是一个已知的接入目标 CLI。
type Target struct {
	ID   string
	Name string // 展示名（CLI 提示用）
}

// TargetIDs 返回全部已知目标 id（CLI usage 提示用）。
func TargetIDs() []string {
	ids := make([]string, 0, len(Targets))
	for _, t := range Targets {
		ids = append(ids, t.ID)
	}
	return ids
}

// Targets 是全部已知接入目标（顺序即 scan 报告顺序）。
var Targets = []Target{
	{"claude", "Claude Code"},
	{"codex", "Codex CLI"},
	{"gemini", "Gemini CLI"},
	{"opencode", "OpenCode"},
	{"pi", "pi"},
}

// Valid 报告 id 是否为已知目标。
func Valid(id string) bool {
	for _, t := range Targets {
		if t.ID == id {
			return true
		}
	}
	return false
}

// backupIfExists 把原文件复制为 <path>.bak-ofd（单代备份），返回备份
// 路径；文件不存在返回空。
func backupIfExists(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	bak := path + ".bak-ofd"
	if err := os.WriteFile(bak, b, 0o600); err != nil {
		return ""
	}
	return bak
}

// bakNote 备份路径的人类可读后缀（空备份返回空串）。
func bakNote(bak string) string {
	if bak == "" {
		return ""
	}
	return "（原文件备份 " + bak + "）"
}

// claudeDir 解析 Claude Code 配置目录（尊重 CLAUDE_CONFIG_DIR）。
func claudeDir(home string) string {
	if d := trimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); d != "" {
		return d
	}
	return filepath.Join(home, ".claude")
}

// codexDir 解析 Codex 配置目录（尊重 CODEX_HOME）。
func codexDir(home string) string {
	if d := trimSpace(os.Getenv("CODEX_HOME")); d != "" {
		return d
	}
	return filepath.Join(home, ".codex")
}

// trimSpace 独立于 strings 以便 tiny helper 复用清晰。
func trimSpace(s string) string { return strings.TrimSpace(s) }
