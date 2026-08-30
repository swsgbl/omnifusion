// tools_health.go 注册 health scope 工具：providers/keys/
// models/health 四个只读视图。
package agent

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerHealthTools 注册 health scope 的工具集。
func registerHealthTools(s *mcp.Server, view *GatewayView) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        toolPrefix + "providers",
		Description: "List gateway providers: model counts, EWMA latency, success rate, last success time, and active isolations (cooldowns/model lockouts).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ProvidersResult, error) {
		out, err := view.Providers(ctx)
		if err != nil {
			return nil, ProvidersResult{}, err
		}
		return textResult(out), *out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        toolPrefix + "keys",
		Description: "List BYOK key sources per provider: stored (keyring), env:VAR, none, or optional. Never returns key material.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, []KeyInfo, error) {
		keys, err := view.Keys(ctx)
		if err != nil {
			return nil, nil, err
		}
		return textResult(keys), keys, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        toolPrefix + "models",
		Description: "List the model catalog: every (provider, model) pair with its context window in tokens.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, []ModelInfo, error) {
		models, err := view.Models(ctx)
		if err != nil {
			return nil, nil, err
		}
		return textResult(models), models, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        toolPrefix + "health",
		Description: "Gateway health snapshot: version, configured provider count, and total active isolations.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, HealthResult, error) {
		out, err := view.Health(ctx)
		if err != nil {
			return nil, HealthResult{}, err
		}
		return textResult(out), *out, nil
	})
}
