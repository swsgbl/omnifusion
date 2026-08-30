// tools_audit.go 注册 audit scope 工具：请求审计日志查询。
package agent

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// AuditArgs 是 audit_recent 的参数。
type AuditArgs struct {
	Limit    int    `json:"limit,omitempty"`    // 默认 20，上限 500
	Provider string `json:"provider,omitempty"` // 过滤赢家 provider
	Endpoint string `json:"endpoint,omitempty"` // chat|messages|gemini
}

// registerAuditTools 注册 audit scope 的工具集。
func registerAuditTools(s *mcp.Server, view *GatewayView) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        toolPrefix + "audit_recent",
		Description: "Show recent request audit log rows (newest first): endpoint, model, winning provider, status, tokens, latency, TTFT, cache hit, error kind.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args AuditArgs) (*mcp.CallToolResult, AuditResult, error) {
		out, err := view.Audit(ctx, args.Limit, args.Provider, args.Endpoint)
		if err != nil {
			return nil, AuditResult{}, err
		}
		return textResult(out), *out, nil
	})
}
