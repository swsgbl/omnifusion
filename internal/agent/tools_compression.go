// tools_compression.go 注册 compression scope 工具：组合
// 清单（含压缩阶段绑定）与默认压缩组合切换——「压缩配置」的运维面。
package agent

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// comboInput 是默认组合工具的输入。
type comboInput struct {
	// Combo 是默认组合名；空字符串 = 清除（请求不再默认走组合）。
	Combo string `json:"combo,omitempty" jsonschema:"combo name to apply as default; empty clears the default"`
}

// registerCompressionTools 注册 compression scope 的工具集。
func registerCompressionTools(s *mcp.Server, view *GatewayView) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        toolPrefix + "combos",
		Description: "List configured combos (named model groups) with their bound compression stages, and the current default combo.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, CombosResult, error) {
		out, err := view.Combos(ctx)
		if err != nil {
			return nil, CombosResult{}, err
		}
		return textResult(out), *out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        toolPrefix + "compression_default",
		Description: "Set or clear the default combo: requests that don't pick a combo explicitly (no @combo directive) get routed and compressed via it. Runtime state; per-request directives always win.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in comboInput) (*mcp.CallToolResult, DefaultComboResult, error) {
		out, err := view.SetDefaultCombo(ctx, in.Combo)
		if err != nil {
			return nil, DefaultComboResult{}, err
		}
		return textResult(out), *out, nil
	})
}
