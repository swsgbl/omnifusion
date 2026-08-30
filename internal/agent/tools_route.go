// tools_route.go 注册 route scope 工具：全局路由钉选（设/
// 清/查）与隔离清除——「路由切换」的运维面。写操作经网关控制 API，
// token 无 route scope 时网关侧 403（工具错误面回传）。
package agent

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pinInput 是钉选工具的输入。
type pinInput struct {
	// Provider 是钉选目标 provider；空字符串 = 清除钉选（unpin）。
	Provider string `json:"provider,omitempty" jsonschema:"target provider name; empty clears the pin"`
	// TTLSeconds 是钉选存活秒数；0 = 网关默认 30 分钟。
	TTLSeconds int `json:"ttl_seconds,omitempty" jsonschema:"pin lifetime in seconds; 0 uses gateway default (30m)"`
}

// clearInput 是隔离清除工具的输入。
type clearInput struct {
	Provider string `json:"provider" jsonschema:"provider name whose isolations should be cleared"`
}

// registerRouteTools 注册 route scope 的工具集。
func registerRouteTools(s *mcp.Server, view *GatewayView) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        toolPrefix + "route_pin",
		Description: "Pin gateway routing to one provider (moved to the front of the try order; failover and isolation still apply), or clear the pin with an empty provider. Runtime state, expires after TTL (default 30m).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pinInput) (*mcp.CallToolResult, PinResult, error) {
		out, err := view.RoutePin(ctx, in.Provider, in.TTLSeconds)
		if err != nil {
			return nil, PinResult{}, err
		}
		return textResult(out), *out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        toolPrefix + "route_status",
		Description: "Show current routing pin (provider, expiry) and active isolation counts per provider.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, RouteStatusResult, error) {
		out, err := view.RouteStatus(ctx)
		if err != nil {
			return nil, RouteStatusResult{}, err
		}
		return textResult(out), *out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        toolPrefix + "route_cooldowns_clear",
		Description: "Manually clear all isolations (cooldowns/model lockouts) for one provider, in memory and persisted state—use to fast-recover a provider after fixing its key or quota.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in clearInput) (*mcp.CallToolResult, ClearResult, error) {
		out, err := view.ClearCooldowns(ctx, in.Provider)
		if err != nil {
			return nil, ClearResult{}, err
		}
		return textResult(out), *out, nil
	})
}
