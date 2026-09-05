// connectcmd.go 是 `ofd connect/disconnect <cli>` 的 CLI 门面：把网关
// 地址与令牌确定性写入各家编码 CLI 的标准配置（Claude Code / Codex /
// Gemini CLI / OpenCode / pi），一条命令完成"接入本项目"。写入器实现
// 在 internal/connect 包（dashboard 管家 API 共用同一份）。三条验收
// 纪律：① 运行时取真值（端口/令牌来自当前配置与密钥派生，绝不写死）；
// ② 定位规则遵守目标工具自己的约定（含其环境变量覆盖）；③ 永远留手
// 动后路（--print 只打印不落盘，写前备份原文件，disconnect 原路清除）。
package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/swsgbl/omnifusion/internal/config"
	"github.com/swsgbl/omnifusion/internal/connect"
	"github.com/swsgbl/omnifusion/internal/security"
)

var connectTargets = connect.TargetIDs()

// runConnectCommand / runDisconnectCommand 是入口；apply 目标工具的
// 写入/清除，disconnect 只是把 mode 参数反过来传给同一套定位逻辑。
func runConnectCommand(cfg *config.Config, args []string) error {
	return runConnect(cfg, args, true)
}

func runDisconnectCommand(cfg *config.Config, args []string) error {
	return runConnect(cfg, args, false)
}

func runConnect(cfg *config.Config, args []string, connectMode bool) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	printOnly := fs.Bool("print", false, "print the exact config instead of writing")
	positional, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return biErr(
			fmt.Sprintf("用法：ofd %s <%s> [--print]", map[bool]string{true: "connect", false: "disconnect"}[connectMode], strings.Join(connectTargets, "|")),
			fmt.Sprintf("usage: ofd %s <claude|codex|gemini|opencode|pi> [--print]", map[bool]string{true: "connect", false: "disconnect"}[connectMode]))
	}
	target := positional[0]
	if !connect.Valid(target) {
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

	var msg string
	if connectMode {
		msg, err = connect.Wire(target, base, token)
	} else {
		msg, err = connect.Unwire(target)
	}
	if err != nil {
		return err
	}
	if *printOnly {
		preview, perr := connect.Plan(target, base, token, !connectMode)
		if perr == nil {
			fmt.Println(preview)
		}
		return nil
	}
	fmt.Println(msg)
	fmt.Printf("（撤销: ofd disconnect %s · 手动配置: ofd connect %s --print）\n", target, target)
	return nil
}

// clientBaseURL 归一化客户端应指向的网关根地址（connect.Origin 统一
// 规则，管家 API 与 CLI 写入的目标保持一致）。
func clientBaseURL(cfg *config.Config) string {
	return connect.Origin(cfg.Server.Host, cfg.Server.Port)
}
