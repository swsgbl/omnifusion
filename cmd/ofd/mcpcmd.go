// mcpcmd.go 实现 `ofd mcp`：stdio 传输的 MCP server，供
// Claude Code 等 MCP 客户端以命令行形态接入（claude mcp add ofd --
// ofd mcp）。数据经 GatewayView 走本机网关 dashboard API；网关 key
// 默认从 keyring 派生（与网关进程同源），--token/OFD_GATEWAY_TOKEN
// 供测试与远程网关场景覆盖。
package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/swsgbl/omnifusion/internal/agent"
	"github.com/swsgbl/omnifusion/internal/config"
	"github.com/swsgbl/omnifusion/internal/security"
)

// runMCPCommand 装配 stdio MCP server 并阻塞运行。
func runMCPCommand(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	gatewayURL := fs.String("gateway-url", "", "网关基地址（默认取配置文件的 server.host:port）")
	token := fs.String("token", "", "网关 API key（默认从 keyring 派生；OFD_GATEWAY_TOKEN 可覆盖）")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("mcp: parse flags: %w", err)
	}

	base := *gatewayURL
	if base == "" {
		base = fmt.Sprintf("http://%s:%d", cfg.Server.Host, cfg.Server.Port)
	}
	if u, err := url.Parse(base); err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("mcp: invalid gateway URL %q", base)
	}

	tok := *token
	if tok == "" {
		tok = os.Getenv("OFD_GATEWAY_TOKEN")
	}
	if tok == "" {
		kr, err := security.Open("")
		if err != nil {
			return fmt.Errorf("mcp: open keyring: %w", err)
		}
		if tok, err = kr.GatewayToken(); err != nil {
			return fmt.Errorf("mcp: derive gateway token: %w", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	view := agent.NewGatewayView(base, tok, nil)
	// 启动即向网关查询本 token 的 scope（whoami）并按 scope
	// 注册工具——越权工具不出现在 tools/list。查询失败（网关未起/
	// token 无效）则 fail-closed 退出：权限未知不暴露任何工具。
	who, err := view.Whoami(ctx)
	if err != nil {
		return fmt.Errorf("mcp: resolve token scopes from gateway %s: %w", base, err)
	}
	scopes := who.Scopes
	if len(scopes) == 0 {
		return fmt.Errorf("mcp: token has no scopes (gateway %s); generate one with 'ofd mcp-token --scopes ...'", base)
	}
	return agent.RunStdio(ctx, agent.NewMCPServer(view, version, scopes))
}
