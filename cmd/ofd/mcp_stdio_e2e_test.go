// mcp_stdio_e2e_test.go 是 M5.1/M5.2 stdio 传输端到端验收：编译真实
// `ofd mcp` 子进程，用 SDK 客户端（CommandTransport，Claude Code 以
// stdio 形态接入的同款路径）连接——启动即 Whoami（M5.2 fail-closed：
// 权限未知不暴露工具）、initialize、tools/list（master 全量 10 工具；
// scoped token 只见授权工具）、tools/call 走本机假网关全链路。
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/server"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// e2eMaster/e2eHealthOnly 是假网关认识的两个 token：master 全 scope，
// scoped 只读健康面。
const (
	e2eMaster     = "ofg-e2e"
	e2eHealthOnly = "ofm-e2e-health-only-token"
)

// newUpstreamForMCP 起校验 Bearer 的假网关（dashboard API + whoami），
// 返回基地址。
func newUpstreamForMCP(t *testing.T) string {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := r.Header.Get("Authorization")
		var scopes []string
		switch bearer {
		case "Bearer " + e2eMaster:
			scopes = server.AllScopes
		case "Bearer " + e2eHealthOnly:
			scopes = []string{server.ScopeHealth}
		default:
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/dashboard/api/whoami":
			kind := "scoped"
			if len(scopes) == len(server.AllScopes) {
				kind = "master"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": kind, "scopes": scopes})
		case "/dashboard/api/usage":
			_, _ = w.Write([]byte(`{"usage":[{"provider":"groq","rpm":1,"rpd":1,"tpm":10,"tpd":10,` +
				`"limits":{"rpm":30,"rpd":14400,"tpm":0,"tpd":0},"headroom":0.97}],"cache_entries":7}`))
		case "/dashboard/api/providers":
			_, _ = w.Write([]byte(`{"providers":[],"models_total":0}`))
		case "/dashboard/api/keys":
			_, _ = w.Write([]byte(`{"keys":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(up.Close)
	return up.URL
}

// buildOfd 编译真实二进制（e2e 共用）。
func buildOfd(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ofd-mcp-e2e.exe")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "GOFLAGS=")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build ofd: %v\n%s", err, out)
	}
	return bin
}

func TestMCPPStdioE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the real ofd binary")
	}
	gwURL := newUpstreamForMCP(t)
	bin := buildOfd(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.Command(bin, "mcp", "-gateway-url", gwURL, "-token", e2eMaster)
	transport := &mcp.CommandTransport{Command: cmd}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "claude-code-stdio-e2e", Version: "v0"}, nil).
		Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("stdio connect: %v", err)
	}
	defer cs.Close()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(tools.Tools) != 11 {
		t.Errorf("tools/list = %d, want 11 (master = all scopes)", len(tools.Tools))
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "omnifusion_usage"})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if res.IsError {
		t.Fatalf("usage call = error result: %+v", res)
	}
	text := ""
	if tc, ok := res.Content[0].(*mcp.TextContent); ok {
		text = tc.Text
	}
	var payload struct {
		CacheEntries int64 `json:"cache_entries"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("TextContent not usage JSON: %v (%s)", err, text)
	}
	if payload.CacheEntries != 7 {
		t.Errorf("cache_entries = %d, want 7", payload.CacheEntries)
	}
}

// TestMCPStdioScopedTokenE2E 是 M5.2 stdio 越权验收：scoped token 启动
// → whoami 只授 health → tools/list 只见 4 工具，call route 工具被拒。
func TestMCPStdioScopedTokenE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the real ofd binary")
	}
	gwURL := newUpstreamForMCP(t)
	bin := buildOfd(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.Command(bin, "mcp", "-gateway-url", gwURL, "-token", e2eHealthOnly)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "claude-code-stdio-scoped-e2e", Version: "v0"}, nil).
		Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("stdio connect (scoped): %v", err)
	}
	defer cs.Close()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(tools.Tools) != 4 {
		t.Fatalf("scoped tools/list = %d, want 4 health tools", len(tools.Tools))
	}
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "omnifusion_route_pin", Arguments: map[string]any{"provider": "x"},
	}); err == nil {
		t.Fatal("scoped stdio session calling route tool must be rejected")
	}
}
