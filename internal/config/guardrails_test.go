// guardrails_test.go 覆盖护栏段：加载与默认关闭语义。
package config

import (
	"os"
	"testing"
)

func TestLoadGuardrailsYAML(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/gr.yaml"
	yaml := `server: {host: 127.0.0.1, port: 20130}
store: {path: db.sqlite}
guardrails:
  enabled: true
  pii:
    action: warn
    types: [email, phone_cn]
  injection:
    action: block
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Guardrails.Enabled || cfg.Guardrails.PII.Action != "warn" ||
		cfg.Guardrails.Injection.Action != "block" {
		t.Errorf("guardrails section = %+v", cfg.Guardrails)
	}
	if len(cfg.Guardrails.PII.Types) != 2 || cfg.Guardrails.PII.Types[0] != "email" {
		t.Errorf("pii types = %v", cfg.Guardrails.PII.Types)
	}
}

func TestGuardrailsDisabledByDefault(t *testing.T) {
	if LoadDefaultGuardrailsEnabled(t) {
		t.Error("guardrails must be opt-in (disabled by default)")
	}
}

func LoadDefaultGuardrailsEnabled(t *testing.T) bool {
	t.Helper()
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load default: %v", err)
	}
	return cfg.Guardrails.Enabled
}
