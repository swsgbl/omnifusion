// mcp_test.go 是 M5.1 Streamable HTTP 端到端验收：用 SDK 客户端
// （StreamableClientTransport，与 Claude Code 同款传输）连真网关的
// /mcp——initialize、tools/list、tools/call 全链路，外加鉴权
// （Bearer 通过、bare 401）。
package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/agent"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// bearerClient 是带网关 key 的 HTTP 客户端（MCP HTTP 客户端注入用）。
type bearerClient struct{ key string }

func (b bearerClient) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+b.key)
	return http.DefaultTransport.RoundTrip(r)
}

// newMCPGateway 起一个挂了 /mcp 的被测网关。MCP 工具的 GatewayView
// 需要网关自身地址（httptest 随机端口），故先起服务拿 URL、再装配
// /mcp 并重挂 handler——与 main.go 的顺序（全部 Set 后才 Handler()）
// 语义一致。M5.2 起 /mcp 用 ScopedHTTPHandler（按请求 token scope
// 构造 server），master key 即全量工具。
func newMCPGateway(t *testing.T) string {
	t.Helper()
	gw, s, _ := newDashFixture(t, &routing.Router{})
	view := agent.NewGatewayView(gw.URL, testGatewayToken, nil)
	s.SetMCPHandler(agent.ScopedHTTPHandler(view, "test", s.ResolveRequestScopes))
	gw.Config.Handler = s.Handler() // 含 /mcp 的完整路由
	return gw.URL
}

func TestMCPEndpointStreamableHTTPE2E(t *testing.T) {
	base := newMCPGateway(t)
	hc := &http.Client{Transport: bearerClient{key: testGatewayToken}, Timeout: 10 * time.Second}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "claude-code-e2e", Version: "v0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint: base + "/mcp", HTTPClient: hc,
		}, nil)
	if err != nil {
		t.Fatalf("MCP client connect: %v", err)
	}
	defer cs.Close()
	ctx := context.Background()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(tools.Tools) != 11 { // 4 health + 1 usage + 3 route + 2 compression + 1 audit
		t.Errorf("tools/list = %d tools, want 11", len(tools.Tools))
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "omnifusion_providers"})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if res.IsError {
		t.Errorf("providers call = error result: %+v", res)
	}
}

// TestMCPEndpointScopedTokenE2E 是 M5.2 Streamable HTTP 越权验收：
// health-only scoped token 能 initialize/tools-list（只见 4 个 health
// 工具），直接 call route 工具被拒（未注册）。
func TestMCPEndpointScopedTokenE2E(t *testing.T) {
	base := newMCPGateway(t)
	healthTok := DeriveMCPToken(testGatewayToken, []string{ScopeHealth})
	hc := &http.Client{Transport: bearerClient{key: healthTok}, Timeout: 10 * time.Second}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "scoped-e2e", Version: "v0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint: base + "/mcp", HTTPClient: hc,
		}, nil)
	if err != nil {
		t.Fatalf("scoped MCP client connect: %v", err)
	}
	defer cs.Close()
	ctx := context.Background()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	if len(tools.Tools) != 4 || !names["omnifusion_providers"] || names["omnifusion_usage"] {
		t.Fatalf("scoped tools/list = %v, want 4 health tools only", names)
	}

	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "omnifusion_route_pin", Arguments: map[string]any{"provider": "x"},
	}); err == nil {
		t.Fatal("scoped token calling route tool must be rejected")
	}

	// 授权工具照常可用（数据回网关自身 dashboard API）。
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "omnifusion_health"})
	if err != nil || res.IsError {
		t.Fatalf("scoped health call: %v %+v", err, res)
	}
}

// TestMCPEndpointRequiresGatewayKey 验证 /mcp 鉴权 fail-closed：裸
// JSON-RPC 请求（initialize）直接 401，不进 SDK 逻辑。
func TestMCPEndpointRequiresGatewayKey(t *testing.T) {
	base := newMCPGateway(t)
	resp, err := http.Post(base+"/mcp", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"bare","version":"v0"}}}`))
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bare initialize = %d, want 401", resp.StatusCode)
	}
}
