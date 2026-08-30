// metrics_audit_test.go 覆盖 metrics/audit 段：加载与默认开启。
package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMetricsAuditYAML：metrics/audit 段可显式关闭并覆盖 max_rows。
func TestMetricsAuditYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "of.yaml")
	yaml := `server: {host: 127.0.0.1, port: 20130}
store: {path: db.sqlite}
metrics: {enabled: false}
audit: {enabled: false, max_rows: 500}
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Metrics.Enabled {
		t.Error("metrics.enabled should parse to false")
	}
	if cfg.Audit.Enabled || cfg.Audit.MaxRows != 500 {
		t.Errorf("audit section = %+v", cfg.Audit)
	}
}

// TestMetricsAuditEnabledByDefault：被动观测默认开启（与 guardrails 的
// opt-in 相反——不改请求行为）。
func TestMetricsAuditEnabledByDefault(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load default: %v", err)
	}
	if !cfg.Metrics.Enabled || !cfg.Audit.Enabled {
		t.Error("metrics/audit must be on by default")
	}
	if cfg.Audit.MaxRows != 10000 {
		t.Errorf("audit.max_rows default = %d, want 10000", cfg.Audit.MaxRows)
	}
}
