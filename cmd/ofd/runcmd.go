// runcmd.go 实现 `ofd run <claude|codex|gemini> [args...]`（
// FreeRide bind 移植）：解析网关地址与 key → 探测 /healthz → 本机
// 未起网关则 autospawn 本二进制（serve 形态，8s 等待）→ 注入目标
// CLI 的环境变量后拉起（参数透传、退出码透传）。
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/swsgbl/omnifusion/internal/agent"
	"github.com/swsgbl/omnifusion/internal/config"
	"github.com/swsgbl/omnifusion/internal/security"
)

// runRunCommand 是 run 子命令入口；cfgPath 供 autospawn 以同一配置
// 起 serve（空串=内置默认值，与用户本次运行同源）。
func runRunCommand(cfg *config.Config, cfgPath string, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	gatewayURL := fs.String("gateway-url", "", "网关基地址（默认取配置文件的 server.host:port）")
	token := fs.String("token", "", "网关 API key（默认从 keyring 派生；OFD_GATEWAY_TOKEN 可覆盖）")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("run: parse flags: %w", err)
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("run: missing CLI target (usage: ofd run <claude|codex|gemini> [args...])")
	}
	target, cliArgs := rest[0], rest[1:]

	base := *gatewayURL
	if base == "" {
		base = fmt.Sprintf("http://%s:%d", cfg.Server.Host, cfg.Server.Port)
	}
	if u, err := url.Parse(base); err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("run: invalid gateway URL %q", base)
	}
	_, err := agent.TargetEnv(target, base, "probe-token") // 先校验目标名
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}

	tok := *token
	if tok == "" {
		tok = os.Getenv("OFD_GATEWAY_TOKEN")
	}
	if tok == "" {
		kr, err := security.Open("")
		if err != nil {
			return fmt.Errorf("run: open keyring: %w", err)
		}
		if tok, err = kr.GatewayToken(); err != nil {
			return fmt.Errorf("run: derive gateway token: %w", err)
		}
	}

	// 吞掉 Ctrl-C/SIGTERM 默认行为：CLI 子进程与 run 同控制台，用户在
	// TUI 里按 Ctrl-C 应由子进程自行处理，包装进程不抢先退出（FreeRide
	// 用 execvpe 换镜像规避此问题；Windows 无 exec，改为信号兜底）。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := agent.ProbeGateway(ctx, base); err != nil {
		if !isLoopbackBase(base) {
			_, _ = fmt.Fprintf(os.Stderr, "ofd: warning: gateway %s unreachable (%v); launching %s anyway\n", base, err, target)
		} else if aerr := ensureGateway(ctx, cfg, cfgPath, base); aerr != nil {
			return fmt.Errorf("run: %w", aerr)
		}
	}

	env, _ := agent.TargetEnv(target, base, tok)
	code, err := agent.LaunchCLI(target, env, cliArgs)
	if err != nil {
		return err
	}
	os.Exit(code)
	return nil
}

// ensureGateway autospawn 本二进制（无子命令=serve 形态）并等待健康，
// FreeRide 语义：8s 超时。serve 输出重定向到 store 目录下的日志文件
// （不污染 CLI 的 TUI）。
func ensureGateway(ctx context.Context, cfg *config.Config, cfgPath, base string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("autospawn gateway: %w", err)
	}
	logDir := filepath.Dir(cfg.Store.Path)
	if logDir == "" || logDir == "." {
		logDir = os.TempDir()
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("autospawn gateway: create log dir: %w", err)
	}
	serveArgs := []string{}
	if cfgPath != "" {
		serveArgs = append(serveArgs, "--config", cfgPath)
	}
	if err := spawnDetached(exe, serveArgs, filepath.Join(logDir, "ofd-serve.log")); err != nil {
		return fmt.Errorf("autospawn gateway: %w", err)
	}
	_, _ = fmt.Fprintf(os.Stderr, "ofd: gateway not running; spawned 'ofd serve' in background (log: %s)\n",
		filepath.Join(logDir, "ofd-serve.log"))
	if err := waitHealthy(ctx, base, 8*time.Second); err != nil {
		return fmt.Errorf("gateway did not become healthy after autospawn: %w", err)
	}
	return nil
}

// waitHealthy 轮询 /healthz 直到超时（250ms 间隔）。
func waitHealthy(ctx context.Context, base string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if last = agent.ProbeGateway(ctx, base); last == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return last
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// isLoopbackBase 判断网关地址是否本机（决定可否 autospawn）。
func isLoopbackBase(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// spawnDetached 后台启动 exe（输出重定向 logPath），父进程退出后存活。
func spawnDetached(exe string, args []string, logPath string) error {
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = detachedAttr()
	if err := cmd.Start(); err != nil {
		_ = logf.Close()
		return err
	}
	go func() { _ = cmd.Wait(); _ = logf.Close() }() // 回收（POSIX 防僵尸）
	return nil
}
