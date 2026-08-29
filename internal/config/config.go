// Package config 加载 YAML 配置并展开 ${VAR} 环境变量引用。
package config

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// Config 是 ofd 的顶层配置。
type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Store      StoreConfig      `yaml:"store"`
	Log        LogConfig        `yaml:"log"`
	Combos     CombosConfig     `yaml:"combos"`
	Fusion     FusionConfig     `yaml:"fusion"`
	Semantic   SemanticConfig   `yaml:"semantic"`
	MLRouter   MLRouterConfig   `yaml:"mlrouter"`
	Catalog    CatalogConfig    `yaml:"catalog"`
	Guardrails GuardrailsConfig `yaml:"guardrails"`
	Metrics    MetricsConfig    `yaml:"metrics"`
	Audit      AuditConfig      `yaml:"audit"`
	A2A        A2AConfig        `yaml:"a2a"`
}

// Default 返回内置默认配置。
func Default() *Config {
	return &Config{
		Server:  ServerConfig{Host: "127.0.0.1", Port: 20130},
		Store:   StoreConfig{Path: "data/omnifusion.db"},
		Log:     LogConfig{Level: "info", Format: "json"},
		Metrics: MetricsConfig{Enabled: true},
		Audit:   AuditConfig{Enabled: true, MaxRows: 10000},
		A2A:     A2AConfig{Enabled: true},
	}
}

// Load 加载配置：path 为空时使用默认值，否则读取 YAML（先展开 ${VAR}）。
func Load(path string) (*Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(expandEnv(data), cfg); err != nil {
			return nil, fmt.Errorf("parse yaml: %w", err)
		}
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnv 只展开 ${VAR} 形式的引用；未定义的变量保留原样，
// 交由 Validate/使用方报错，避免 os.ExpandEnv 把裸 $ 全部吞掉。
func expandEnv(data []byte) []byte {
	return envPattern.ReplaceAllFunc(data, func(m []byte) []byte {
		name := envPattern.FindSubmatch(m)[1]
		if v, ok := os.LookupEnv(string(name)); ok {
			return []byte(v)
		}
		return m
	})
}
