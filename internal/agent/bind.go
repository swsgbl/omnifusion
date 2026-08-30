// bind.go 实现 CLI 包装（流程移植自 FreeRide `freeride run <cli>`，
// MIT — Copyright (c) 2026 Shaishav Pidadi，见根目录 NOTICE；本文件为
// Go 原创重写）：
// 为 claude/codex/gemini CLI 子进程注入网关环境变量——一条命令把
// 官方 CLI 指向本网关（ofd run claude）。目标→环境变量映射、健康
// 探测与进程拉起都在本文件；网关地址/token 解析在 cmd/ofd 层。
package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"
)

// cliTargets 是受支持的 CLI 目标（验收：claude|codex|gemini）。
var cliTargets = []string{"claude", "codex", "gemini"}

// CLITargets 返回受支持目标（有序）。
func CLITargets() []string {
	out := make([]string, len(cliTargets))
	copy(out, cliTargets)
	return out
}

// TargetEnv 定义每个目标的注入变量：baseURL 是网关基地址（无尾斜杠），
// token 是网关 API key。三个官方 CLI 的追加规则与网关挂载点对齐：
//
//	claude: {base}/v1/messages （ANTHROPIC_AUTH_TOKEN 发 Bearer）
//	codex: {base}/v1/chat/completions （OPENAI_BASE_URL 需含 /v1）
//	gemini: {base}/v1beta/models/{m}:... （GEMINI_API_KEY 发 x-goog-api-key）
func TargetEnv(target, baseURL, token string) ([]string, error) {
	switch target {
	case "claude":
		return []string{
			"ANTHROPIC_BASE_URL=" + baseURL,
			"ANTHROPIC_AUTH_TOKEN=" + token,
		}, nil
	case "codex":
		return []string{
			"OPENAI_BASE_URL=" + baseURL + "/v1",
			"OPENAI_API_KEY=" + token,
		}, nil
	case "gemini":
		return []string{
			"GOOGLE_GEMINI_BASE_URL=" + baseURL,
			"GEMINI_API_KEY=" + token,
		}, nil
	default:
		return nil, fmt.Errorf("unknown CLI target %q (supported: claude, codex, gemini)", target)
	}
}

// probeTimeout 是单次 /healthz 探测的超时。
const probeTimeout = 2 * time.Second

// ProbeGateway 探测网关健康（GET {baseURL}/healthz 期望 200）。
// 供 run 命令决定是否 autospawn，以及 CLI 拉起前的可用性提示。
func ProbeGateway(ctx context.Context, baseURL string) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz status %d", resp.StatusCode)
	}
	return nil
}

// LaunchCLI 以 PATH 查找目标 CLI 并带着注入变量拉起（stdio 继承，
// 参数透传），返回其退出码。ExitError 是子进程自身退出的正常路径。
func LaunchCLI(target string, extraEnv, args []string) (int, error) {
	path, err := exec.LookPath(target)
	if err != nil {
		return 0, fmt.Errorf("CLI %q not found in PATH; install it first (e.g. npm install -g @anthropic-ai/claude-code): %w", target, err)
	}
	cmd := exec.Command(path, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return 0, fmt.Errorf("launch %s: %w", target, err)
	}
	return 0, nil
}
