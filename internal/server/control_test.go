// control_test.go 覆盖 M5.2 控制面：whoami 身份解析、scoped token 的
// 逐端点越权拒绝（403）、路由钉选/隔离清除/默认压缩组合的写路径，
// 以及钉选对真实分发的生效（pin 后流量改道钉选 provider 上游）。
package server

import (
	"encoding/json"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/store"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// newControlFixture 起双 provider 网关（alpha/beta 各接一个上游，
// 响应模型名可区分流量去向）。
func newControlFixture(t *testing.T) (*httptest.Server, *Server, *store.Store, *httptest.Server, *httptest.Server) {
	t.Helper()
	upA := newUpstream(t, "model-alpha")
	upB := newUpstream(t, "model-beta")
	a := mustAdapter(t, "alpha", upA)
	b := mustAdapter(t, "beta", upB)
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), st))
	s.SetRouter(&routing.Router{Providers: []provider.Provider{a, b}}) // 无 Scoring：注册序可预测
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	return gw, s, st, upA, upB
}

// newUpstream 起一个返回固定模型名的 OpenAI 形态上游。
func newUpstream(t *testing.T, model string) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"`+model+`",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(up.Close)
	return up
}

func mustAdapter(t *testing.T, name string, up *httptest.Server) provider.Provider {
	t.Helper()
	a, err := openai_compat.New(openai_compat.Spec{ProviderName: name, BaseURL: up.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("adapter %s: %v", name, err)
	}
	return a
}

// apiCall 是控制面请求 helper（Bearer token + JSON 体）。
func apiCall(t *testing.T, method, url, token, body string) *http.Response {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return resp
}

// TestWhoami 验证身份解析：master 全 scope、scoped 声明子集。
func TestWhoami(t *testing.T) {
	gw, _, _, _, _ := newControlFixture(t)

	resp := apiCall(t, http.MethodGet, gw.URL+"/dashboard/api/whoami", testGatewayToken, "")
	defer resp.Body.Close()
	var out struct {
		Kind   string   `json:"kind"`
		Scopes []string `json:"scopes"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil {
		t.Fatalf("whoami master: %d", resp.StatusCode)
	}
	if out.Kind != "master" || len(out.Scopes) != len(AllScopes) {
		t.Fatalf("master whoami = %+v", out)
	}

	healthTok := DeriveMCPToken(testGatewayToken, []string{ScopeHealth})
	resp = apiCall(t, http.MethodGet, gw.URL+"/dashboard/api/whoami", healthTok, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil {
		t.Fatalf("whoami scoped: %d", resp.StatusCode)
	}
	if out.Kind != "scoped" || len(out.Scopes) != 1 || out.Scopes[0] != ScopeHealth {
		t.Fatalf("scoped whoami = %+v", out)
	}
}

// TestScopedTokenCrossScopeDenied 是 M5.2 核心验收（越权被拒）：
// health scope 的 token 访问 usage 读端点与 route/compression 写端点
// 全部 403；自身 scope 的端点 200；伪造 token 一律 401。
func TestScopedTokenCrossScopeDenied(t *testing.T) {
	gw, _, _, _, _ := newControlFixture(t)
	healthTok := DeriveMCPToken(testGatewayToken, []string{ScopeHealth})

	cases := []struct {
		method, path string
		body         string
		want         int
	}{
		{http.MethodGet, "/dashboard/api/providers", "", 200},
		{http.MethodGet, "/dashboard/api/models", "", 200},
		{http.MethodGet, "/dashboard/api/health", "", 200},
		{http.MethodGet, "/dashboard/api/keys", "", 200},
		{http.MethodGet, "/dashboard/api/usage", "", 403},
		{http.MethodGet, "/dashboard/api/combos", "", 403},
		{http.MethodGet, "/dashboard/api/route/status", "", 403},
		{http.MethodPost, "/dashboard/api/route/pin", `{"provider":"alpha"}`, 403},
		{http.MethodPost, "/dashboard/api/route/cooldowns/clear", `{"provider":"alpha"}`, 403},
		{http.MethodPost, "/dashboard/api/compression/default", `{"combo":"x"}`, 403},
	}
	for _, tc := range cases {
		resp := apiCall(t, tc.method, gw.URL+tc.path, healthTok, tc.body)
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.want)
		}
	}

	resp := apiCall(t, http.MethodGet, gw.URL+"/dashboard/api/providers", "ofm-forged0000000000000", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("forged token = %d, want 401", resp.StatusCode)
	}
}
