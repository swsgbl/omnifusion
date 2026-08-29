// mcptokencmd_test.go 覆盖 `ofd mcp-token` 的 scope 解析与派生往返。
package main

import (
	"reflect"
	"testing"

	"github.com/swsgbl/omnifusion/internal/server"
)

func TestParseScopes(t *testing.T) {
	if got := parseScopes(""); !reflect.DeepEqual(got, server.AllScopes) {
		t.Fatalf("empty = %v, want all", got)
	}
	if got := parseScopes(" route , health ,, route "); !reflect.DeepEqual(got, []string{"route", "health", "route"}) {
		t.Fatalf("messy = %v", got)
	}
	if got := parseScopes("health,bogus"); got != nil {
		t.Fatalf("unknown scope = %v, want nil", got)
	}
}

func TestMCPTokenDeriveRoundTrip(t *testing.T) {
	master := "ofg-testmaster"
	tok := server.DeriveMCPToken(master, parseScopes("usage,health"))
	if tok == "" {
		t.Fatal("derive = empty")
	}
	scopes, ok := server.ResolveScopes(master, tok)
	if !ok || !reflect.DeepEqual(scopes, []string{"health", "usage"}) {
		t.Fatalf("resolve = %v %v", scopes, ok)
	}
	if scopes2, ok := server.ResolveScopes("ofg-othermaster", tok); ok {
		t.Fatalf("token validates against different master: %v", scopes2)
	}
}
