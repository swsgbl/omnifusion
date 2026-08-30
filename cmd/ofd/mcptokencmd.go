// mcptokencmd.go 实现 `ofd mcp-token`：从 keyring 主密钥派生
// scoped token——HMAC(master, scope 集)，确定性、无存储，随时可重新
// 生成（作废只能换 master，即 keyring 重置）。scoped token 供 MCP
// 客户端（ofd mcp --token / Streamable HTTP Bearer）以最小权限接入。
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/swsgbl/omnifusion/internal/security"
	"github.com/swsgbl/omnifusion/internal/server"
)

// runMCPTokenCommand 解析 --scopes 并打印派生 token 与使用说明。
func runMCPTokenCommand(args []string) error {
	fs := flag.NewFlagSet("mcp-token", flag.ContinueOnError)
	scopesFlag := fs.String("scopes", "", "逗号分隔的作用域子集（health,usage,route,compression）；空 = 全部（等同网关 master key）")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("mcp-token: parse flags: %w", err)
	}

	scopes := parseScopes(*scopesFlag)
	if scopes == nil {
		return fmt.Errorf("mcp-token: unknown scope in %q (known: %s)",
			*scopesFlag, strings.Join(server.AllScopes, ","))
	}

	kr, err := security.Open("")
	if err != nil {
		return fmt.Errorf("mcp-token: open keyring: %w", err)
	}
	master, err := kr.GatewayToken()
	if err != nil {
		return fmt.Errorf("mcp-token: derive gateway token: %w", err)
	}

	token := server.DeriveMCPToken(master, scopes)
	if token == "" {
		return fmt.Errorf("mcp-token: no valid scopes after normalization")
	}
	fmt.Println(token)
	_, _ = fmt.Fprintf(os.Stderr, "scopes: %s\nusage: ofd mcp --token %s…  (or 'Authorization: Bearer %s…' on /mcp)\n",
		strings.Join(server.NormalizeScopes(scopes), ","), token[:8], token[:8])
	return nil
}

// parseScopes 解析逗号分隔的 scope 清单；空 = 全量；含未知项返回 nil。
func parseScopes(s string) []string {
	if strings.TrimSpace(s) == "" {
		return server.AllScopes
	}
	scopes := []string{}
	for _, sc := range strings.Split(s, ",") {
		sc = strings.TrimSpace(sc)
		if sc == "" {
			continue
		}
		if !scopeKnown(sc) {
			return nil
		}
		scopes = append(scopes, sc)
	}
	return scopes
}

// scopeKnown 判定 scope 是否合法。
func scopeKnown(s string) bool {
	for _, k := range server.AllScopes {
		if k == s {
			return true
		}
	}
	return false
}
