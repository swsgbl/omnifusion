package security

import (
	"bytes"
	"testing"
)

func TestKeyringRoundTrip(t *testing.T) {
	kr, err := Open("")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	plain := []byte("sk-super-secret-123")
	ct, err := kr.Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Contains(ct, plain) {
		t.Error("ciphertext contains plaintext")
	}
	got, err := kr.Decrypt(ct)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("roundtrip mismatch: %q", got)
	}
}

func TestKeyringMachineDerivedIsStable(t *testing.T) {
	a, err := Open("")
	if err != nil {
		t.Fatalf("Open a: %v", err)
	}
	b, err := Open("")
	if err != nil {
		t.Fatalf("Open b: %v", err)
	}
	ct, err := a.Encrypt([]byte("payload"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := b.Decrypt(ct); err != nil {
		t.Errorf("same-machine keyring must interoperate: %v", err)
	}
}

func TestKeyringPassphraseMatters(t *testing.T) {
	a, err := Open("pass-a")
	if err != nil {
		t.Fatalf("Open a: %v", err)
	}
	b, err := Open("pass-b")
	if err != nil {
		t.Fatalf("Open b: %v", err)
	}
	ct, err := a.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := b.Decrypt(ct); err == nil {
		t.Error("different passphrase must not decrypt")
	}
	if _, err := a.Decrypt(ct); err != nil {
		t.Errorf("same passphrase must decrypt: %v", err)
	}
}

func TestKeyringTamperDetected(t *testing.T) {
	kr, err := Open("")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ct, err := kr.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	ct[len(ct)-1] ^= 0xff
	if _, err := kr.Decrypt(ct); err == nil {
		t.Error("tampered ciphertext must fail authentication")
	}
}

func TestKeyringRejectsUnknownVersion(t *testing.T) {
	kr, err := Open("")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := kr.Decrypt([]byte{0x99, 1, 2, 3}); err == nil {
		t.Error("unknown version must be rejected")
	}
	if _, err := kr.Decrypt(nil); err == nil {
		t.Error("empty ciphertext must be rejected")
	}
}

func TestOpenPicksUpPassphraseEnv(t *testing.T) {
	t.Setenv(PassphraseEnv, "")
	k0, err := Open("")
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv(PassphraseEnv, "audit-round-3")
	k1, err := Open("") // 空口令自动取环境变量（R5 对策 3 接线）
	if err != nil {
		t.Fatal(err)
	}
	ct, err := k1.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k0.Decrypt(ct); err == nil {
		t.Fatal("env passphrase must change the derived master key (k0 must not decrypt)")
	}
	k2, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k2.Decrypt(ct); err != nil {
		t.Fatalf("same env must derive the same master key: %v", err)
	}
}
