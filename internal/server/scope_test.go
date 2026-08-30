// scope_test.go 覆盖 作用域权限核心：token 派生归一、全部
// 子集的派生→解析往返、master/伪造 token 解析、requireScope 中间件
// 的 401/403/200 三态。
package server

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// TestDeriveMCPTokenNormalizes 验证 scope 归一：乱序/重复/未知输入
// 派生出同一 token；空集派生空串。
func TestDeriveMCPTokenNormalizes(t *testing.T) {
	a := DeriveMCPToken(testGatewayToken, []string{"route", "health", "route", "bogus"})
	b := DeriveMCPToken(testGatewayToken, []string{"health", "route"})
	if a == "" || a != b {
		t.Fatalf("derive not normalized: %q vs %q", a, b)
	}
	if got := DeriveMCPToken(testGatewayToken, nil); got != "" {
		t.Fatalf("empty scopes derive = %q, want empty", got)
	}
	if got := DeriveMCPToken(testGatewayToken, []string{"bogus"}); got != "" {
		t.Fatalf("unknown-only scopes derive = %q, want empty", got)
	}
}

// TestResolveScopesRoundTrip 验证全部 15 个非空子集都能还原。
func TestResolveScopesRoundTrip(t *testing.T) {
	n := len(AllScopes)
	for mask := 1; mask < 1<<n; mask++ {
		want := []string{}
		for i := 0; i < n; i++ {
			if mask&(1<<i) != 0 {
				want = append(want, AllScopes[i])
			}
		}
		tok := DeriveMCPToken(testGatewayToken, want)
		if tok == "" {
			t.Fatalf("derive(%v) = empty", want)
		}
		got, ok := ResolveScopes(testGatewayToken, tok)
		if !ok {
			t.Fatalf("resolve(%v) rejected", want)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("resolve = %v, want %v", got, want)
		}
	}
}

// TestResolveScopesMasterAndUnknown 验证 master 全量与伪造拒绝。
func TestResolveScopesMasterAndUnknown(t *testing.T) {
	got, ok := ResolveScopes(testGatewayToken, testGatewayToken)
	if !ok || !reflect.DeepEqual(got, AllScopes) {
		t.Fatalf("master resolve = %v %v, want all scopes", got, ok)
	}
	if _, ok := ResolveScopes(testGatewayToken, "ofm-deadbeefdeadbeefdead"); ok {
		t.Fatal("forged scoped token accepted")
	}
	if _, ok := ResolveScopes(testGatewayToken, "ofg-anothermastershape"); ok {
		t.Fatal("non-master ofg token accepted")
	}
	if _, ok := ResolveScopes("", DeriveMCPToken(testGatewayToken, AllScopes)); ok {
		t.Fatal("empty master must fail closed")
	}
}

// TestRequireScopeMiddleware 验证 scope 化中间件三态：master 200、
// scoped 匹配 200、scoped 不匹配 403、未知 token 401。
func TestRequireScopeMiddleware(t *testing.T) {
	s := authedServer(New(nil, nil, nil))
	routeTok := DeriveMCPToken(testGatewayToken, []string{ScopeRoute})
	handler := s.requireScope(ScopeRoute, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		name string
		tok  string
		want int
	}{
		{"master", testGatewayToken, 200},
		{"scoped route", routeTok, 200},
		{"scoped health", DeriveMCPToken(testGatewayToken, []string{ScopeHealth}), 403},
		{"forged", "ofm-nope", 401},
		{"empty", "", 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/x", nil)
			if tc.tok != "" {
				req.Header.Set("Authorization", "Bearer "+tc.tok)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
