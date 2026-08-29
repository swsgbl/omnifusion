// tools.go 承载 MCP 工具的共享 helper（M5.2）：TextContent 序列化
// （客户端兼容面最广，结构化数据由 AddTool 的 Out 类型参数下发）。
package agent

import (
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// textResult 把视图序列化为 TextContent JSON。
func textResult(v any) *mcp.CallToolResult {
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte(fmt.Sprintf(`{"error":"marshal: %s"}`, err))
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
	}
}
