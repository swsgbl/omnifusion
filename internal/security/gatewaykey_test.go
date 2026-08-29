package security

import (
	"strings"
	"testing"
)

func TestGatewayTokenDeterministicAndShape(t *testing.T) {
	a, err := Open("")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	b, err := Open("")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ta, err := a.GatewayToken()
	if err != nil {
		t.Fatalf("GatewayToken: %v", err)
	}
	tb, err := b.GatewayToken()
	if err != nil {
		t.Fatalf("GatewayToken: %v", err)
	}
	if ta != tb {
		t.Errorf("gateway token must be deterministic on the same machine/passphrase")
	}
	if !strings.HasPrefix(ta, "ofg-") {
		t.Errorf("token prefix = %q, want ofg-", ta[:4])
	}
	if len(ta) != len("ofg-")+64 {
		t.Errorf("token length = %d, want %d", len(ta), len("ofg-")+64)
	}
	if !IsGatewayTokenShape(ta) {
		t.Errorf("IsGatewayTokenShape(%q...) = false, want true", ta[:8])
	}
	if IsGatewayTokenShape(ta[:len(ta)-1]) {
		t.Errorf("truncated token must fail shape check")
	}
	if IsGatewayTokenShape("sk-not-a-gateway-key") {
		t.Errorf("foreign key must fail shape check")
	}
}

func TestGatewayTokenDiffersWithPassphrase(t *testing.T) {
	a, err := Open("")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	b, err := Open("hunter2")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ta, err := a.GatewayToken()
	if err != nil {
		t.Fatalf("GatewayToken: %v", err)
	}
	tb, err := b.GatewayToken()
	if err != nil {
		t.Fatalf("GatewayToken: %v", err)
	}
	if ta == tb {
		t.Errorf("adding a passphrase must change the derived gateway token")
	}
}
