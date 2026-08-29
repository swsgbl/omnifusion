// guide.go 是小白友好的首启引导（2026-08-29 P0）：检测到尚未录入任何
// 提供商密钥时，在控制台输出三步上手横幅——把「下一步做什么」直接写
// 在用户眼前，而不是指望他们去读文档。双语并排（中文为主，英文附注）。
package main

import (
	"fmt"

	"github.com/swsgbl/omnifusion/internal/config"
)

// printFirstRunGuide 在零密钥时打印上手指引。keySources 为空或全部为
// "none"/"-"（无 stored 密钥、无环境变量密钥）时触发；ollama 等免密钥
// 本地 provider 单独在场也算零云密钥（引导仍然有价值——免费云额度是
// 本项目的核心价值主张）。
func printFirstRunGuide(cfg *config.Config, keySources map[string]string) {
	if len(keySources) == 0 {
		return
	}
	for _, src := range keySources {
		if src != "none" && src != "-" {
			return // 至少一家有密钥：老手，不打扰
		}
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port)
	fmt.Println()
	fmt.Println("┌──────────────────────────────────────────────────────────────────")
	fmt.Println("│  还没有配置任何密钥 · No API keys configured yet")
	fmt.Println("│")
	fmt.Println("│  三步开始免费聊天 · Chat with free models in 3 steps:")
	fmt.Println("│");
	fmt.Println("│    1. 申请免费密钥（任选一家）· Get a free key (any one):")
	fmt.Println("│         https://openrouter.ai/keys")
	fmt.Println("│         CN: open.bigmodel.cn / siliconflow.cn / modelscope.cn")
	fmt.Println("│    2. 录入密钥 · Add it:      ofd key add openrouter")
	fmt.Println("│    3. 打开对话页 · Open chat: " + base + "/dashboard/chat")
	fmt.Println("│       （网关令牌见 ofd gateway-key · gateway token via that command）")
	fmt.Println("│");
	fmt.Println("│  已有 Claude Code / Codex？ · Already use them?")
	fmt.Println("│       ofd run claude    /    ofd run codex     一键接入 · one command")
	fmt.Println("└──────────────────────────────────────────────────────────────────")
	fmt.Println()
}
