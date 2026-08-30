package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRootLanding 验证根路径落地页：200、双语关键内容与入口链接齐备
// （鉴权由路由层豁免——页面无敏感信息）。
func TestRootLanding(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Server{}).handleRoot(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"OmniFusion", "Gateway is running", "网关运行中",
		"/dashboard/chat", "/dashboard", "/v1", "ofd gateway-key",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("landing missing %q", want)
		}
	}
}
