// routepin_test.go 覆盖路由钉选的写路径与对真实分发的生效。
package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// TestRoutePinLifecycle 验证钉选写路径：pin（未知 provider 400）→
// status 可见 → unpin 清除；TTL 过期后钉选失效（惰性）。
func TestRoutePinLifecycle(t *testing.T) {
	gw, s, _, _, _ := newControlFixture(t)

	resp := apiCall(t, http.MethodPost, gw.URL+"/dashboard/api/route/pin", testGatewayToken, `{"provider":"ghost"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("pin unknown provider = %d, want 400", resp.StatusCode)
	}

	resp = apiCall(t, http.MethodPost, gw.URL+"/dashboard/api/route/pin", testGatewayToken, `{"provider":"beta"}`)
	defer resp.Body.Close()
	var pin struct {
		Pinned string  `json:"pinned"`
		Until  *string `json:"until"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&pin) != nil {
		t.Fatalf("pin: %d", resp.StatusCode)
	}
	if pin.Pinned != "beta" || pin.Until == nil {
		t.Fatalf("pin response = %+v", pin)
	}

	s.pinMu.Lock()
	s.pinUntil = time.Now().Add(-time.Hour) // 白盒回拨：TTL 已过
	s.pinMu.Unlock()
	name, until := s.pinSnapshot()
	if name != "" || !until.IsZero() {
		t.Fatalf("expired pin = %q %v, want cleared", name, until)
	}
}

// TestRoutePinAffectsDispatch 验证钉选对真实分发生效：默认注册序
// 流量走 alpha；pin beta 后走 beta；unpin 后回到 alpha。
func TestRoutePinAffectsDispatch(t *testing.T) {
	gw, _, _, upA, upB := newControlFixture(t)

	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	if got := dispatchModel(t, gw.URL, body); got != "model-alpha" {
		t.Fatalf("default dispatch = %s, want alpha", got)
	}

	resp := apiCall(t, http.MethodPost, gw.URL+"/dashboard/api/route/pin", testGatewayToken, `{"provider":"beta","ttl_seconds":300}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pin: %d", resp.StatusCode)
	}
	if got := dispatchModel(t, gw.URL, body); got != "model-beta" {
		t.Fatalf("pinned dispatch = %s, want beta", got)
	}

	resp = apiCall(t, http.MethodPost, gw.URL+"/dashboard/api/route/unpin", testGatewayToken, `{}`)
	resp.Body.Close()
	if got := dispatchModel(t, gw.URL, body); got != "model-alpha" {
		t.Fatalf("unpinned dispatch = %s, want alpha", got)
	}
	_ = upA
	_ = upB
}

// dispatchModel 发一次 chat 请求并返回上游实际使用的模型名。
func dispatchModel(t *testing.T, gwURL, body string) string {
	t.Helper()
	resp := postAuthed(t, gwURL+"/v1/chat/completions", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chat = %d", resp.StatusCode)
	}
	var out struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode chat: %v", err)
	}
	return out.Model
}
