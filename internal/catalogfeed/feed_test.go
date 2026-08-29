package catalogfeed

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

const testFeedJSON = `{
  "version": 3,
  "generated_at": 1700000000,
  "providers": {
    "demo": {
      "free_tier": "1k req/day",
      "models": [
        {"id": "m-a", "ctx_len": 8192, "status": "stable"},
        {"id": "m-b", "ctx_len": 4096, "status": "probation"}
      ]
    }
  }
}`

func TestSignVerifyRoundTrip(t *testing.T) {
	seed, pub, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if len(seed) != 64 || len(pub) != 64 {
		t.Fatalf("key lengths: seed %d pub %d, want 64/64", len(seed), len(pub))
	}
	sig, pubOut, err := Sign([]byte(testFeedJSON), seed)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if len(sig) != 128 {
		t.Fatalf("sig length %d, want 128", len(sig))
	}
	if pubOut != pub {
		t.Fatalf("Sign pubkey %s != GenerateKey pubkey %s", pubOut, pub)
	}
	pk, err := ParsePublicKey(pub)
	if err != nil {
		t.Fatalf("ParsePublicKey: %v", err)
	}
	if err := Verify([]byte(testFeedJSON), sig, pk); err != nil {
		t.Fatalf("Verify round trip: %v", err)
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	seed, pub, _ := GenerateKey()
	sig, _, err := Sign([]byte(testFeedJSON), seed)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	pk, _ := ParsePublicKey(pub)

	tampered := bytes.Replace([]byte(testFeedJSON), []byte("8192"), []byte("9999"), 1)
	if err := Verify(tampered, sig, pk); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered body: err = %v, want ErrBadSignature", err)
	}

	// 换一对密钥签：pinned 公钥拒收。
	otherSeed, _, _ := GenerateKey()
	otherSig, _, _ := Sign([]byte(testFeedJSON), otherSeed)
	if err := Verify([]byte(testFeedJSON), otherSig, pk); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("wrong key: err = %v, want ErrBadSignature", err)
	}

	// 坏签名编码。
	if err := Verify([]byte(testFeedJSON), "zz"+sig[2:], pk); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("bad sig hex: err = %v, want ErrBadSignature", err)
	}
}

func TestParseFeedValidation(t *testing.T) {
	if _, err := ParseFeed([]byte(testFeedJSON)); err != nil {
		t.Fatalf("valid feed rejected: %v", err)
	}
	bad := map[string]string{
		"version 为零":  strings.Replace(testFeedJSON, `"version": 3`, `"version": 0`, 1),
		"空 providers": strings.Replace(testFeedJSON, `"demo"`, `""`, 1),
		"空 model id":  strings.Replace(testFeedJSON, `"m-a"`, `""`, 1),
		"负 ctx_len":   strings.Replace(testFeedJSON, `"ctx_len": 8192`, `"ctx_len": -1`, 1),
		"坏 status":    strings.Replace(testFeedJSON, `"status": "stable"`, `"status": "beta"`, 1),
		"非 JSON":      "not json",
	}
	for name, raw := range bad {
		if _, err := ParseFeed([]byte(raw)); err == nil {
			t.Errorf("%s: ParseFeed accepted bad feed", name)
		}
	}
	// providers 空 map / 缺 models。
	if _, err := ParseFeed([]byte(`{"version":1,"generated_at":1,"providers":{}}`)); err == nil {
		t.Error("empty providers map accepted")
	}
	if _, err := ParseFeed([]byte(`{"version":1,"generated_at":1,"providers":{"p":{"models":[]}}}`)); err == nil {
		t.Error("provider without models accepted")
	}
	// capability 字段（quality 策略数据源）：合法区间 [0,100]，越界拒收。
	if _, err := ParseFeed([]byte(`{"version":1,"generated_at":1,"providers":{"p":{"models":[` +
		`{"id":"m","ctx_len":8,"status":"stable","capability":87.5}]}}}`)); err != nil {
		t.Fatalf("valid capability rejected: %v", err)
	}
	for _, cap := range []string{"-0.1", "100.5"} {
		raw := `{"version":1,"generated_at":1,"providers":{"p":{"models":[` +
			`{"id":"m","ctx_len":8,"status":"stable","capability":` + cap + `}]}}}`
		if _, err := ParseFeed([]byte(raw)); err == nil {
			t.Errorf("capability %s accepted, want rejected", cap)
		}
	}
	// price_in/price_out（cheap 真成本数据源）：显式 0 = 免费声明，
	// 负值/半配对拒收（指针语义同注册表）。
	if _, err := ParseFeed([]byte(`{"version":1,"generated_at":1,"providers":{"p":{"models":[` +
		`{"id":"m","ctx_len":8,"status":"stable","price_in":0,"price_out":0}]}}}`)); err != nil {
		t.Fatalf("explicit free price rejected: %v", err)
	}
	for name, model := range map[string]string{
		"负 price":     `{"id":"m","ctx_len":8,"status":"stable","price_in":-1,"price_out":2}`,
		"半配对 price": `{"id":"m","ctx_len":8,"status":"stable","price_in":1}`,
	} {
		raw := `{"version":1,"generated_at":1,"providers":{"p":{"models":[` + model + `]}}}`
		if _, err := ParseFeed([]byte(raw)); err == nil {
			t.Errorf("%s: ParseFeed accepted bad price", name)
		}
	}
}

func TestCheckFreshness(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	old := &Feed{GeneratedAt: now.Add(-time.Hour).Unix()}
	if err := old.CheckFreshness(now); err != nil {
		t.Fatalf("past feed rejected: %v", err)
	}
	edge := &Feed{GeneratedAt: now.Add(MaxClockSkew - time.Minute).Unix()}
	if err := edge.CheckFreshness(now); err != nil {
		t.Fatalf("within skew rejected: %v", err)
	}
	future := &Feed{GeneratedAt: now.Add(MaxClockSkew + time.Hour).Unix()}
	if err := future.CheckFreshness(now); err == nil {
		t.Fatal("future feed accepted")
	}
}

func TestParsePublicKey(t *testing.T) {
	if _, err := ParsePublicKey("not-hex!"); err == nil {
		t.Error("non-hex key accepted")
	}
	if _, err := ParsePublicKey(strings.Repeat("ab", 31)); err == nil {
		t.Error("short key accepted")
	}
	seed, pub, _ := GenerateKey()
	if _, err := ParsePublicKey("  " + pub + "  "); err != nil {
		t.Fatalf("key with surrounding whitespace rejected: %v", err)
	}
	_ = seed
}

func TestSignRejectsBadSeed(t *testing.T) {
	if _, _, err := Sign([]byte("x"), "short"); err == nil {
		t.Error("short seed accepted")
	}
	if _, _, err := Sign([]byte("x"), strings.Repeat("zz", 32)); err == nil {
		t.Error("non-hex seed accepted")
	}
}
