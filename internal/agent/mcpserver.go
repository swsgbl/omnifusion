// mcpserver.go 是 MCP Server 构造层（M5.1 骨架 + M5.2 作用域权限）：
// NewMCPServer 按 token 的 scope 集注册工具——越权工具不出现在
// tools/list，直接 call 得到 SDK 的 tool-not-found 错误（最小暴露）。
// 传输层双形态：stdio（`ofd mcp` 独立进程，RunStdio——启动时先
// Whoami 确定 scope，fail-closed）与 Streamable HTTP（ScopedHTTPHandler
// 按请求 token 构造 per-scope server 实例并缓存）。
package agent

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolName 前缀统一为 omnifusion_，避免与客户端其他 MCP server 撞名。
const toolPrefix = "omnifusion_"

// Scope 常量与网关侧 server.AllScopes 对齐（两层间的字符串契约，
// server 端 e2e 测试锁定）。空 scope 集构造出无工具 server——
// 权限未知即不暴露（fail-closed）。
const (
	ScopeHealth      = "health"
	ScopeUsage       = "usage"
	ScopeRoute       = "route"
	ScopeCompression = "compression"
	ScopeAudit       = "audit"
)

// AllScopes 是 agent 侧 scope 全集（master token 的工具面）。
var AllScopes = []string{ScopeHealth, ScopeUsage, ScopeRoute, ScopeCompression, ScopeAudit}

// NewMCPServer 构造注册了 scope 内工具集的 MCP server；version 来自
// 构建信息（cmd/ofd 注入，与网关 /healthz 同源）。scopes 为空时注册
// 零个工具（fail-closed）；全量需显式传 AllScopes。
func NewMCPServer(view *GatewayView, version string, scopes []string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "ofd", Version: version}, nil)
	has := func(scope string) bool {
		for _, sc := range scopes {
			if sc == scope {
				return true
			}
		}
		return false
	}
	if has(ScopeHealth) {
		registerHealthTools(s, view)
	}
	if has(ScopeUsage) {
		registerUsageTools(s, view)
	}
	if has(ScopeRoute) {
		registerRouteTools(s, view)
	}
	if has(ScopeCompression) {
		registerCompressionTools(s, view)
	}
	if has(ScopeAudit) {
		registerAuditTools(s, view)
	}
	return s
}

// RunStdio 以 stdio 传输运行 MCP server（阻塞至 ctx 取消或对端关闭；
// `ofd mcp` 的入口）。
func RunStdio(ctx context.Context, s *mcp.Server) error {
	return s.Run(ctx, &mcp.StdioTransport{})
}

// ScopedHTTPHandler 返回按请求 token scope 构造 server 的 Streamable
// HTTP 传输（挂网关 /mcp）：scopesFor 由 main 注入网关侧解析；同一
// scope 集的 server 实例缓存复用（工具无会话态，会话管理在 SDK
// handler 内）。解析失败兜底返回无工具 server（外层中间件已挡
// 401，此处防御性降级）。
func ScopedHTTPHandler(view *GatewayView, version string, scopesFor func(*http.Request) ([]string, bool)) http.Handler {
	var mu sync.Mutex
	cache := map[string]*mcp.Server{}
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		scopes, ok := scopesFor(r)
		if !ok {
			scopes = nil
		}
		key := strings.Join(scopes, ",")
		mu.Lock()
		s, hit := cache[key]
		mu.Unlock()
		if hit {
			return s
		}
		s = NewMCPServer(view, version, scopes)
		mu.Lock()
		cache[key] = s
		mu.Unlock()
		return s
	}, nil)
}
