package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfigIsValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("默认配置应合法: %v", err)
	}
	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("默认监听必须是回环地址, got %q", cfg.Server.Host)
	}
}

func TestLoadYAMLAndEnvExpand(t *testing.T) {
	t.Setenv("OFD_TEST_PORT", "20456")
	content := `
server:
  host: 127.0.0.1
  port: ${OFD_TEST_PORT}
log:
  level: debug
  format: text
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.Server.Port != 20456 {
		t.Errorf("env 展开失败, port = %d, want 20456", cfg.Server.Port)
	}
	if cfg.Log.Level != "debug" || cfg.Log.Format != "text" {
		t.Errorf("yaml 覆盖失败: %+v", cfg.Log)
	}
	// 未覆盖字段保留默认值（store 为每用户规范绝对路径）。
	if !filepath.IsAbs(cfg.Store.Path) {
		t.Errorf("默认 store 路径应为绝对路径（每用户规范位置）: %q", cfg.Store.Path)
	}
	if want := filepath.Join("OmniFusion", "data", "omnifusion.db"); !strings.HasSuffix(filepath.ToSlash(cfg.Store.Path), filepath.ToSlash(want)) {
		t.Errorf("默认 store 路径缺少规范尾段 %q: %q", want, cfg.Store.Path)
	}
}

func TestLoadUndefinedEnvKeptVerbatim(t *testing.T) {
	content := "server:\n  host: ${OFD_UNDEFINED_VAR_XYZ}\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("未定义 env 展开为非法 host，应校验报错")
	}
}

func TestValidateRejectsBadPort(t *testing.T) {
	cfg := Default()
	cfg.Server.Port = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("port=0 应报错")
	}
}
