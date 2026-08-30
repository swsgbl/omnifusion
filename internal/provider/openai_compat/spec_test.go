// spec_test.go 覆盖 Spec 校验与传输层代理行为。
package openai_compat

import (
	"net/http"
	"testing"
)

func TestSpecValidation(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Spec)
	}{
		{"missing name", func(s *Spec) { s.ProviderName = "" }},
		{"missing base url", func(s *Spec) { s.BaseURL = "" }},
		{"header style without header name", func(s *Spec) { s.AuthStyle = AuthHeader }},
		{"query style without param name", func(s *Spec) { s.AuthStyle = AuthQuery }},
		{"unknown auth style", func(s *Spec) { s.AuthStyle = "weird" }},
		{"unknown max_tokens policy", func(s *Spec) { s.MaxTokens = "weird" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := baseSpec()
			tc.mut(&s)
			if _, err := New(s); err == nil {
				t.Error("expected validation error")
			}
		})
	}
}

// TestTransportsUseEnvProxy 钉住 L2 分级代理的全局层接线：
// 两个 client 的 Transport 必须挂 ProxyFromEnvironment，否则
// HTTPS_PROXY/HTTP_PROXY 形式的分级代理对区域封锁上游（Groq
// CN/HK 403）失效。行为面由 1.9 双源冒烟实测覆盖。
func TestTransportsUseEnvProxy(t *testing.T) {
	a, err := New(baseSpec())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for name, cl := range map[string]*http.Client{"client": a.client, "streamClient": a.streamClient} {
		tr, ok := cl.Transport.(*http.Transport)
		if !ok {
			t.Fatalf("%s: Transport = %T, want *http.Transport", name, cl.Transport)
		}
		if tr.Proxy == nil {
			t.Errorf("%s: Transport.Proxy is nil; env proxy (HTTPS_PROXY/HTTP_PROXY) would be ignored", name)
		}
	}
}
