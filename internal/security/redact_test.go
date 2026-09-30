// redact_test.go 锁死脱敏契约：标准密钥形态全灭（含 Bearer/键值
// 上下文保留）、良性文本零误伤、幂等。
package security

import (
	"strings"
	"testing"
)

func TestRedactSecretsKeyShapes(t *testing.T) {
	cases := map[string]string{
		"Authorization: Bearer sk-abc123def456ghi789":                                                     "Authorization: Bearer [REDACTED]",
		"bearer hf_xyz987654321":                                                                          "bearer [REDACTED]",
		`{"error":"invalid api_key: sk-or-v1-deadbeefcafe1234"}`:                                          `{"error":"invalid api_key: [REDACTED]"}`,
		`"token": "ghp_1234567890abcdefghij"`:                                                             `"token": "[REDACTED]"`,
		"key=ark-083d98a2-435f-416f-a1d9-43ad17eff677":                                                    "key=[REDACTED]",
		"leak AIzaSyA1234567890abcdefghijk in body":                                                       "leak [REDACTED] in body",
		"token=xoxb-1234567890abcdef-GHIJKL":                                                              "token=[REDACTED]",
		"ofg-0123456789abcdef0123456789abcdef":                                                            "[REDACTED]",
		"sha256 deadbeefdeadbeefdeadbeefdeadbeefdeadbeef":                                                 "sha256 [REDACTED]",
		"jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJVadQssw5c": "jwt [REDACTED]",
	}
	for in, want := range cases {
		if got := RedactSecrets(in); got != want {
			t.Errorf("RedactSecrets(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedactSecretsBenignUntouched(t *testing.T) {
	benign := []string{
		"upstream mock returned status 500",
		"model gpt-4o-mini is rate limited",
		"region-blocked? set HTTPS_PROXY to a permitted region",
		"quota window exhausted for provider zhipu",
		"visit https://api.example.com/v1/models for docs",
		"short sk- prefix alone stays",
		"bearer without value",
		"token", // 裸词无值
		"checksum abc123",
	}
	for _, in := range benign {
		if got := RedactSecrets(in); got != in {
			t.Errorf("benign text mangled: %q -> %q", in, got)
		}
	}
}

func TestRedactSecretsIdempotent(t *testing.T) {
	in := "Bearer sk-abcdefghijklmnopqrst and api_key: sk-proj-0987654321abc"
	once := RedactSecrets(in)
	twice := RedactSecrets(once)
	if once != twice {
		t.Fatalf("not idempotent: %q vs %q", once, twice)
	}
	if strings.Contains(once, "sk-abcdefghijklmnopqrst") || strings.Contains(once, "sk-proj-0987654321abc") {
		t.Fatalf("secrets survived: %q", once)
	}
}
