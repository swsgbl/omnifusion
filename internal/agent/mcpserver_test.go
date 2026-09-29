// mcpserver_test.go 以 MCP 客户端（InMemory 传输，SDK 同款连接路径）
// 验证 // 工具集：全量 14 工具、scope 过滤后 tools/list 只见
// 授权工具、越权 call 被 SDK 以 tool-not-found 拒绝、授权工具回传
// 网关数据。
package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// dialMCP 用 InMemory 传输把 MCP 客户端连到被测 server（模拟 Claude
// Code 的连接行为）。
func dialMCP(t *testing.T, s *mcp.Server) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "claude-code-test", Version: "v0"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := s.Connect(context.Background(), t1, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := c.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// toolNames 取 tools/list 的名字集合。
func toolNames(t *testing.T, cs *mcp.ClientSession) map[string]bool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("tool %s has no description", tool.Name)
		}
	}
	return names
}

// TestMCPServerExposesFullToolset 验证 master（全 scope）的 14 工具。
func TestMCPServerExposesFullToolset(t *testing.T) {
	up, _ := newFakeGateway(t, 200)
	s := NewMCPServer(NewGatewayView(up.URL, "tok", nil), "test", AllScopes)
	names := toolNames(t, dialMCP(t, s))

	want := []string{
		"omnifusion_providers", "omnifusion_keys", "omnifusion_models", "omnifusion_health",
		"omnifusion_usage",
		"omnifusion_route_pin", "omnifusion_route_status", "omnifusion_route_cooldowns_clear",
		"omnifusion_combos", "omnifusion_compression_default",
		"omnifusion_audit_recent",
		"omnifusion_task_get", "omnifusion_task_update", "omnifusion_task_cancel",
	}
	if len(names) != len(want) {
		t.Fatalf("tools/list = %d tools, want %d (%v)", len(names), len(want), names)
	}
	for _, w := range want {
		if !names[w] {
			t.Errorf("tools/list missing %s", w)
		}
	}
}

// TestMCPServerScopeFiltersToolset 是 核心验收（工具面收敛）：
// health-only scope 只见 4 个 health 工具；直接 call route 工具被
// SDK 拒绝（tool not found——未注册即不可调）。
func TestMCPServerScopeFiltersToolset(t *testing.T) {
	up, _ := newFakeGateway(t, 200)
	s := NewMCPServer(NewGatewayView(up.URL, "tok", nil), "test", []string{ScopeHealth})
	names := toolNames(t, dialMCP(t, s))

	if len(names) != 4 {
		t.Fatalf("health-only tools/list = %v, want 4 tools", names)
	}
	for _, w := range []string{"omnifusion_providers", "omnifusion_keys", "omnifusion_models", "omnifusion_health"} {
		if !names[w] {
			t.Errorf("health-only tools/list missing %s", w)
		}
	}
	for _, banned := range []string{"omnifusion_usage", "omnifusion_route_pin", "omnifusion_route_cooldowns_clear", "omnifusion_combos", "omnifusion_compression_default"} {
		if names[banned] {
			t.Errorf("health-only tools/list must not expose %s", banned)
		}
	}
}

// TestMCPServerUnauthorizedToolCallRejected 验证越权调用被拒：health
// scope 的会话 call route 工具 → SDK 错误（未注册工具不可调）。
func TestMCPServerUnauthorizedToolCallRejected(t *testing.T) {
	up, _ := newFakeGateway(t, 200)
	s := NewMCPServer(NewGatewayView(up.URL, "tok", nil), "test", []string{ScopeHealth})
	cs := dialMCP(t, s)

	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "omnifusion_route_pin",
		Arguments: map[string]any{"provider": "groq"},
	}); err == nil {
		t.Fatal("unauthorized tools/call must fail (tool not registered for this scope)")
	}
}

// TestMCPServerEmptyScopesRegistersNothing 验证 fail-closed：空 scope
// 构造出无工具 server（权限未知即零暴露）。
func TestMCPServerEmptyScopesRegistersNothing(t *testing.T) {
	up, _ := newFakeGateway(t, 200)
	s := NewMCPServer(NewGatewayView(up.URL, "tok", nil), "test", nil)
	if names := toolNames(t, dialMCP(t, s)); len(names) != 0 {
		t.Fatalf("empty scopes tools/list = %v, want none", names)
	}
}

func TestMCPServerCallToolsReturnsGatewayData(t *testing.T) {
	up, _ := newFakeGateway(t, 200)
	s := NewMCPServer(NewGatewayView(up.URL, "tok", nil), "test", AllScopes)
	cs := dialMCP(t, s)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "omnifusion_providers"})
	if err != nil {
		t.Fatalf("tools/call providers: %v", err)
	}
	if res.IsError {
		t.Fatalf("providers call returned error result: %+v", res)
	}
	var text string
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	var ps ProvidersResult
	if err := json.Unmarshal([]byte(text), &ps); err != nil {
		t.Fatalf("TextContent is not the providers JSON: %v (%s)", err, text)
	}
	if len(ps.Providers) != 1 || ps.Providers[0].Name != "groq" || ps.ModelsTotal != 6 {
		t.Errorf("providers payload = %+v", ps)
	}

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "omnifusion_usage"})
	if err != nil {
		t.Fatalf("tools/call usage: %v", err)
	}
	if res.IsError {
		t.Fatalf("usage call returned error result: %+v", res)
	}
	text = ""
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	var us UsageResult
	if err := json.Unmarshal([]byte(text), &us); err != nil {
		t.Fatalf("TextContent is not the usage JSON: %v (%s)", err, text)
	}
	if us.CacheEntries != 7 || len(us.Usage) != 1 {
		t.Errorf("usage payload = %+v", us)
	}
}

// TestMCPServerRouteToolsCallGateway 验证写工具经控制面回传：route
// scope 会话调 route_pin → 网关收到 POST + token。
func TestMCPServerRouteToolsCallGateway(t *testing.T) {
	up, seen := newFakeGateway(t, 200)
	s := NewMCPServer(NewGatewayView(up.URL, "tok-9", nil), "test", []string{ScopeRoute})
	cs := dialMCP(t, s)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "omnifusion_route_pin",
		Arguments: map[string]any{"provider": "groq", "ttl_seconds": 120},
	})
	if err != nil {
		t.Fatalf("tools/call route_pin: %v", err)
	}
	if res.IsError {
		t.Fatalf("route_pin returned error result: %+v", res)
	}
	if seen == nil || *seen != "Bearer tok-9" {
		t.Fatalf("gateway Authorization = %v, want Bearer tok-9", seen)
	}
}

func TestMCPServerToolErrorOnUnreachableGateway(t *testing.T) {
	s := NewMCPServer(NewGatewayView("http://127.0.0.1:1", "tok", nil), "test", AllScopes)
	cs := dialMCP(t, s)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "omnifusion_keys"})
	if err != nil {
		t.Fatalf("tools/call transport: %v", err)
	}
	if !res.IsError {
		t.Errorf("unreachable gateway should surface as tool error, got %+v", res)
	}
}

// TestAuditScopeToolsetAndCall：audit-only scope 只注册 audit_recent，
// 工具调用经 GatewayView 回读 dashboard API。
func TestAuditScopeToolsetAndCall(t *testing.T) {
	up, _ := newFakeGateway(t, 200)
	s := NewMCPServer(NewGatewayView(up.URL, "tok", nil), "test", []string{ScopeAudit})
	cs := dialMCP(t, s)
	defer cs.Close()

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "omnifusion_audit_recent" {
		t.Fatalf("audit-only tools = %d, want exactly omnifusion_audit_recent", len(tools.Tools))
	}

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "omnifusion_audit_recent", Arguments: struct{}{},
	})
	if err != nil {
		t.Fatalf("tools/call audit_recent: %v", err)
	}
	if res.IsError {
		t.Fatalf("audit_recent returned error: %+v", res)
	}
	if len(res.Content) == 0 {
		t.Fatal("audit_recent returned no content")
	}
}

// TestTasksScopeToolsetAndCall：tasks-only scope 只注册三个 task 工具，
// 经 MCP 客户端走完整生命周期（get→update(start/complete)），并验证
// 越权面收敛（health 工具不可见）。
func TestTasksScopeToolsetAndCall(t *testing.T) {
	up, _ := newFakeGateway(t, 200)
	s := NewMCPServer(NewGatewayView(up.URL, "tok", nil), "test", []string{ScopeTasks})
	cs := dialMCP(t, s)
	defer cs.Close()

	names := toolNames(t, cs)
	for _, w := range []string{"omnifusion_task_get", "omnifusion_task_update", "omnifusion_task_cancel"} {
		if !names[w] {
			t.Errorf("tasks-only scope missing %s", w)
		}
	}
	if len(names) != 3 {
		t.Fatalf("tasks-only scope exposes %d tools, want 3 (%v)", len(names), names)
	}
	if names["omnifusion_health"] {
		t.Fatal("tasks-only scope must not expose health tools")
	}

	store := DefaultTasks()
	task, _, err := store.Create("mcp-e2e", "", 0)
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}

	callTask := func(tool string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatalf("tools/call %s: %v", tool, err)
		}
		return res
	}

	if res := callTask("omnifusion_task_update", map[string]any{"id": task.ID, "action": "start"}); res.IsError {
		t.Fatalf("task_update start errored: %+v", res)
	}
	res := callTask("omnifusion_task_get", map[string]any{"id": task.ID})
	if res.IsError {
		t.Fatalf("task_get errored: %+v", res)
	}
	var got Task
	if err := json.Unmarshal([]byte(textOf(t, res)), &got); err != nil {
		t.Fatalf("decode task_get payload: %v", err)
	}
	if got.ID != task.ID || got.Status != TaskStatusRunning {
		t.Fatalf("task_get = %+v, want running %s", got, task.ID)
	}
	if res := callTask("omnifusion_task_cancel", map[string]any{"id": task.ID}); res.IsError {
		t.Fatalf("task_cancel errored: %+v", res)
	}
	if final, _ := store.Get(task.ID); final.Status != TaskStatusCanceled {
		t.Fatalf("task not canceled: %+v", final)
	}
}

// textOf 提取 TextContent 结果的首段文本。
func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("tool returned no content")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("tool content is not text: %T", res.Content[0])
	}
	return tc.Text
}
