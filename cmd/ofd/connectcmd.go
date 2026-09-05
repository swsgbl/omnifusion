// connectcmd.go 是 `ofd connect/disconnect <cli>`：把网关地址与令牌
// 确定性写入各家编码 CLI 的标准配置（Claude Code / Codex / Gemini CLI /
// OpenCode），一条命令完成"接入本项目"。三条验收纪律：① 运行时取真值
//（端口/令牌来自当前配置与密钥派生，绝不写死）；② 定位规则遵守目标
// 工具自己的约定（含其环境变量覆盖）；③ 永远留手动后路（--print 只打
// 印不落盘，写前备份原文件，disconnect 原路清除）。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/swsgbl/omnifusion/internal/config"
	"github.com/swsgbl/omnifusion/internal/security"
)

var connectTargets = []string{"claude", "codex", "gemini", "opencode", "pi"}

// runConnectCommand / runDisconnectCommand 是入口；apply 目标工具的
// 写入/清除，disconnect 只是把 mode 参数反过来传给同一套定位逻辑。
func runConnectCommand(cfg *config.Config, args []string) error {
	return runConnect(cfg, args, true)
}

func runDisconnectCommand(cfg *config.Config, args []string) error {
	return runConnect(cfg, args, false)
}

func runConnect(cfg *config.Config, args []string, connect bool) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	printOnly := fs.Bool("print", false, "print the exact config instead of writing")
	positional, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return biErr(
			fmt.Sprintf("用法：ofd %s <claude|codex|gemini|opencode|pi> [--print]", map[bool]string{true: "connect", false: "disconnect"}[connect]),
			fmt.Sprintf("usage: ofd %s <claude|codex|gemini|opencode|pi> [--print]", map[bool]string{true: "connect", false: "disconnect"}[connect]))
	}
	target := positional[0]
	known := false
	for _, t := range connectTargets {
		if t == target {
			known = true
		}
	}
	if !known {
		return biErr(
			fmt.Sprintf("未知客户端 %q（支持：%s）", target, strings.Join(connectTargets, ", ")),
			fmt.Sprintf("unknown cli %q (supported: %s)", target, strings.Join(connectTargets, ", ")))
	}

	kr, err := security.Open("")
	if err != nil {
		return fmt.Errorf("open keyring: %w", err)
	}
	token, err := kr.GatewayToken()
	if err != nil {
		return fmt.Errorf("derive gateway token: %w", err)
	}
	base := clientBaseURL(cfg)

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home: %w", err)
	}
	var msg string
	switch target {
	case "claude":
		dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
		if dir == "" {
			dir = filepath.Join(home, ".claude")
		}
		msg, err = applyClaude(filepath.Join(dir, "settings.json"), base, token, connect, *printOnly)
	case "codex":
		dir := strings.TrimSpace(os.Getenv("CODEX_HOME"))
		if dir == "" {
			dir = filepath.Join(home, ".codex")
		}
		msg, err = applyCodex(dir, base, token, connect, *printOnly)
	case "gemini":
		msg, err = applyGemini(filepath.Join(home, ".gemini", ".env"), base, token, connect, *printOnly)
	case "opencode":
		msg, err = applyOpenCode(home, base, token, connect, *printOnly)
	case "pi":
		msg, err = applyPi(filepath.Join(home, ".pi", "agent", "models.json"), base, token, connect, *printOnly)
	}
	if err != nil {
		return err
	}
	fmt.Println(msg)
	fmt.Printf("（撤销: ofd disconnect %s · 手动配置: ofd connect %s --print）\n", target, target)
	return nil
}

// clientBaseURL 归一化客户端应指向的网关根地址：监听通配/空地址时
// 客户端走回环；端口取当前配置真值。
func clientBaseURL(cfg *config.Config) string {
	host := cfg.Server.Host
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, fmt.Sprint(cfg.Server.Port))
}

// backupIfExists 把原文件复制为 <path>.bak-ofd（单代备份，connect 与
// disconnect 写前都执行），返回备份路径；文件不存在返回空。
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

// applyClaude 写 ~/.claude/settings.json 的 env 块（官方 LLM gateway
// 接入口径：ANTHROPIC_BASE_URL + ANTHROPIC_AUTH_TOKEN；base 为根地址，
// SDK 自行追加 /v1/messages——网关的 Anthropic 入站原生承接）。
func applyClaude(path, base, token string, connect, printOnly bool) (string, error) {
	m := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &m); err != nil {
			return "", fmt.Errorf("parse %s: %w（手动处理见 ofd connect claude --print）", path, err)
		}
	}
	env, _ := m["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	if connect {
		env["ANTHROPIC_BASE_URL"] = base
		env["ANTHROPIC_AUTH_TOKEN"] = token
	} else {
		delete(env, "ANTHROPIC_BASE_URL")
		delete(env, "ANTHROPIC_AUTH_TOKEN")
	}
	if len(env) == 0 {
		delete(m, "env")
	} else {
		m["env"] = env
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
	return fmt.Sprintf("%s %s%s（重启 claude 生效）", map[bool]string{true: "已写入", false: "已清除"}[connect], path, bakNote(bak)), nil
}

func bakNote(bak string) string {
	if bak == "" {
		return ""
	}
	return "（原文件备份 " + bak + "）"
}
