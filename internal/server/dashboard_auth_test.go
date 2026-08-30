package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDashboardKeyBrowserAuthPage 浏览器导航（Accept: text/html）未带
// key 时应回双语 HTML 指引页；页面内 fetch（Accept: */*）保持 JSON——
// 裸 JSON 出现在 iframe 里是 v0.1.3 实测的小白体验缺陷。
func TestDashboardKeyBrowserAuthPage(t *testing.T) {
	s := &Server{}
	s.SetGatewayToken(testGatewayToken)
	h := s.requireDashboardKey(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 浏览器导航：HTML 页（含中英指引与获取命令）。
	req := httptest.NewRequest(http.MethodGet, "/dashboard/providers", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("browser nav code = %d, want 401", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Error("browser nav should return text/html")
	}
	for _, want := range []string{"网关令牌", "ofd gateway-key", "从 ofd 读取 Key"} {
		if !strings.Contains(body, want) {
			t.Errorf("auth page missing %q", want)
		}
	}
	if strings.Contains(body, `"authentication_error"`) {
		t.Error("browser nav should not receive raw JSON error")
	}

	// 页面内 fetch：保持 JSON 错误（前端 err 逻辑依赖它）。
	req2 := httptest.NewRequest(http.MethodGet, "/dashboard/api/providers", nil)
	req2.Header.Set("Accept", "*/*")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("fetch code = %d, want 401", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), `"authentication_error"`) {
		t.Error("fetch should keep JSON error body")
	}

	// 正确 key：通过。
	req3 := httptest.NewRequest(http.MethodGet, "/dashboard/providers?key="+testGatewayToken, nil)
	req3.Header.Set("Accept", "text/html")
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("valid key code = %d, want 200", rec3.Code)
	}
}
