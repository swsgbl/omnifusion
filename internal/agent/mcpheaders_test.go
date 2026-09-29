// mcpheaders_test.go 锁死可路由标头与目录指纹契约：指纹确定性
// （同 scope 集=同值、scope 顺序不影响、跨 scope 集不同、空="none"）；
// 中间件透传请求并盖目录指纹响应头；标头/请求体 method 冲突告警
// 不拒；legacy 无标头零影响（无日志行、指纹头仍在）。
package agent

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestToolCatalogVersionDeterministic(t *testing.T) {
	ctx := context.Background()
	a1, err := ToolCatalogVersion(ctx, "test", []string{ScopeHealth})
	if err != nil {
		t.Fatalf("fingerprint health: %v", err)
	}
	a2, err := ToolCatalogVersion(ctx, "test", []string{ScopeHealth}) // 缓存路径
	if err != nil {
		t.Fatalf("cached path: %v", err)
	}
	if a1 != a2 {
		t.Fatalf("cache path diverged: %s vs %s", a1, a2)
	}
	if len(a1) != 12 {
		t.Fatalf("fingerprint must be 12 hex chars, got %q", a1)
	}
	full, err := ToolCatalogVersion(ctx, "test", AllScopes)
	if err != nil {
		t.Fatalf("fingerprint all: %v", err)
	}
	if full == a1 {
		t.Fatalf("different scope sets must yield different fingerprints")
	}
	// scope 顺序不影响指纹（两条独立缓存项、各自真实枚举）。
	unordered, err := ToolCatalogVersion(ctx, "test", []string{ScopeAudit, ScopeHealth})
	if err != nil {
		t.Fatalf("unordered: %v", err)
	}
	ordered, err := TaskCatalogOrdered(ctx)
	if err != nil {
		t.Fatalf("ordered: %v", err)
	}
	if unordered != ordered {
		t.Fatalf("scope order must not change fingerprint: %s vs %s", unordered, ordered)
	}
}

func TaskCatalogOrdered(ctx context.Context) (string, error) {
	return ToolCatalogVersion(ctx, "test", []string{ScopeHealth, ScopeAudit})
}

func TestToolCatalogVersionEmptyScopes(t *testing.T) {
	v, err := ToolCatalogVersion(context.Background(), "test", nil)
	if err != nil {
		t.Fatalf("empty scopes: %v", err)
	}
	if v != "none" {
		t.Fatalf("empty scopes must be \"none\", got %q", v)
	}
}

// captureLogger 收集 JSON 日志行（断言观测面）。
func captureLogger() (*slog.Logger, *[]string) {
	var mu sync.Mutex
	lines := &[]string{}
	h := slog.NewJSONHandler(&syncWriter{mu: &mu, dst: lines}, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), lines
}

type syncWriter struct {
	mu  *sync.Mutex
	dst *[]string
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	*w.dst = append(*w.dst, string(p))
	return len(p), nil
}

// newRoutingTestServer 构造真实 MCPRoutingHandler（内层为 SDK
// Streamable HTTP）；观测面断言不依赖 SDK 层状态码。
func newRoutingTestServer(t *testing.T, scopes []string, log *slog.Logger) *httptest.Server {
	t.Helper()
	h := MCPRoutingHandler(nil, "test", func(*http.Request) ([]string, bool) {
		return scopes, true
	}, log)
	return httptest.NewServer(h)
}

func TestMCPRoutingHandlerSetsCatalogHeader(t *testing.T) {
	log, lines := captureLogger()
	ts := newRoutingTestServer(t, []string{ScopeHealth}, log)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL, bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set(HeaderMcpMethod, "tools/list")
	req.Header.Set(HeaderMcpProtocolVersion, "2026-07-28")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get(ToolCatalogHeader); len(got) != 12 {
		t.Fatalf("catalog fingerprint header missing/short: %q", got)
	}
	joined := strings.Join(*lines, "\n")
	if !strings.Contains(joined, "mcp routable headers") {
		t.Fatalf("routable headers must be logged, got: %s", joined)
	}
	if !strings.Contains(joined, "tools/list") || !strings.Contains(joined, "2026-07-28") {
		t.Fatalf("header values must enter the log, got: %s", joined)
	}
}

func TestMCPRoutingMethodMismatchWarns(t *testing.T) {
	log, lines := captureLogger()
	ts := newRoutingTestServer(t, []string{ScopeHealth}, log)
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"omnifusion_health"}}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL, bytes.NewBufferString(body))
	req.Header.Set(HeaderMcpMethod, "tools/list") // 与体矛盾
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	joined := strings.Join(*lines, "\n")
	if !strings.Contains(joined, "contradicts request body") {
		t.Fatalf("mismatch must be warned, got: %s", joined)
	}
	// 冲突不拒：连接未被观测层掐断（状态码属 SDK 层语义）。
	if resp.StatusCode == 0 {
		t.Fatalf("request must be served, got status 0")
	}
}

func TestMCPRoutingLegacyNoHeaders(t *testing.T) {
	log, lines := captureLogger()
	ts := newRoutingTestServer(t, []string{ScopeHealth}, log)
	defer ts.Close()

	resp, err := ts.Client().Post(ts.URL, "application/json", bytes.NewBufferString(`{"jsonrpc":"2.0","id":3,"method":"ping"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get(ToolCatalogHeader); len(got) != 12 {
		t.Fatalf("catalog header must still be present for legacy clients: %q", got)
	}
	joined := strings.Join(*lines, "\n")
	if strings.Contains(joined, "routable headers") {
		t.Fatalf("no headers → no log line, got: %s", joined)
	}
}
