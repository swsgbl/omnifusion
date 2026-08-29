// tools_usage.go 注册 usage scope 工具（M5.2）：配额滑窗用量统计。
package agent

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerUsageTools 注册 usage scope 的工具集。
func registerUsageTools(s *mcp.Server, view *GatewayView) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        toolPrefix + "usage",
		Description: "Show per-provider quota sliding-window usage (RPM/RPD/TPM/TPD used vs limits, headroom) and the semantic cache entry count.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, UsageResult, error) {
		out, err := view.Usage(ctx)
		if err != nil {
			return nil, UsageResult{}, err
		}
		return textResult(out), *out, nil
	})
}
