// Package config 加载 YAML 配置并展开 ${VAR} 环境变量引用。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"

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
	// Providers 是用户自定义 provider 声明：同 id 覆盖内置声明（如改
	// base_url/密钥环境变量），新 id 追加进注册表——任意 OpenAI 兼容
	//（或 anthropic/gemini 原生协议）厂商零代码接入。字段与内置
	// providers/*.yaml 同构（cmd/ofd 转换后并入 registry）。
	Providers []ProviderConfig `yaml:"providers"`
}

// Default 返回内置默认配置。Catalog feed 默认指向官方签名目录
//（仓库 catalog/feed.json + .sig 边车，公钥 pin 死）：@quality 能力
// 排序与窗口数据开箱即用；摄取失败只降级不阻断（feed 是增强数据），
// 内网/离线场景可在配置里显式置空两项关闭。
// Store 路径默认为每用户规范位置（DefaultStorePath）——终端、桌面端、
// 任何启动方式都读写同一份数据（密钥/隔离/缓存单一正本）。
func Default() *Config {
	return &Config{
		Server:  ServerConfig{Host: "127.0.0.1", Port: 20130},
		Store:   StoreConfig{Path: DefaultStorePath()},
		Log:     LogConfig{Level: "info", Format: "json"},
		Metrics: MetricsConfig{Enabled: true},
		Audit:   AuditConfig{Enabled: true, MaxRows: 10000},
		A2A:     A2AConfig{Enabled: true},
		Catalog: CatalogConfig{
			FeedURL: "https://raw.githubusercontent.com/swsgbl/omnifusion/main/catalog/feed.json",
			FeedPubkey: "0ac49b691b1632cbd9c121bc79dbbff2a6c69243134ff6ce5c78eafc8cd46de8",
		},
	}
}

// DefaultStorePath 返回每用户规范数据路径（与启动方式/工作目录无关的
// 唯一一处）：
//
//	Windows  %LOCALAPPDATA%\OmniFusion\data\omnifusion.db
//	macOS    ~/Library/Application Support/OmniFusion/data/omnifusion.db
//	其他     $XDG_DATA_HOME/OmniFusion/data/omnifusion.db（缺省 ~/.local/share）
//
// 解析失败（极端环境）回落旧的相对默认 data/omnifusion.db——跟随工作
// 目录，行为与历史一致。显式配置 store.path 的用户不受本函数影响。
func DefaultStorePath() string {
	base := ""
	switch runtime.GOOS {
	case "windows":
		base = os.Getenv("LOCALAPPDATA")
		if base == "" {
			if ucfg, err := os.UserConfigDir(); err == nil {
				base = ucfg
			}
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, "Library", "Application Support")
		}
	default:
		base = os.Getenv("XDG_DATA_HOME")
		if base == "" {
			if home, err := os.UserHomeDir(); err == nil {
				base = filepath.Join(home, ".local", "share")
			}
		}
	}
	if base == "" {
		return "data/omnifusion.db"
	}
	return filepath.Join(base, "OmniFusion", "data", "omnifusion.db")
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
