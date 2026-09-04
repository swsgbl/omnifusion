package config

// providers_config.go 是用户自定义 provider 声明（yaml providers: 段）：
// 与内置 providers/*.yaml 同构（registry 包的 Entry 同字段同 tag），
// 同 id 覆盖内置、新 id 追加——任意 OpenAI 兼容/原生协议厂商零代码
// 接入。本包是依赖叶子，不 import registry：cmd/ofd 经 yaml 往返转换
// 后交给 registry.LoadWith 合并（两套类型字段漂移由
// TestProviderConfigRoundtrip 守卫）。

// ProviderConfig 是一个自定义 provider 声明。
type ProviderConfig struct {
	ID          string   `yaml:"id"`
	Kind        string   `yaml:"kind"` // openai_compat | anthropic | gemini
	DisplayName string   `yaml:"display_name"`
	BaseURL     string   `yaml:"base_url"`
	Path        string   `yaml:"path,omitempty"`
	AuthStyle   string   `yaml:"auth_style,omitempty"`
	KeyEnv      string   `yaml:"key_env,omitempty"`
	URLVars     []string `yaml:"url_vars,omitempty"`
	VarsEnv     map[string]string `yaml:"vars_env,omitempty"`
	OptionalKey bool     `yaml:"optional_key,omitempty"`
	ExtraHeaders map[string]string `yaml:"extra_headers,omitempty"`
	ModelAliases map[string]string `yaml:"model_aliases,omitempty"`
	RateLimits   RateLimitsConfig   `yaml:"rate_limits,omitempty"`
	Capabilities CapabilitiesConfig `yaml:"capabilities"`
	FreeTier     string   `yaml:"free_tier,omitempty"`
	Models       []ModelDeclConfig  `yaml:"models"`
}

// RateLimitsConfig 是免费层滑动窗口声明（0/缺省 = 不限）。
type RateLimitsConfig struct {
	RPM int   `yaml:"rpm,omitempty"`
	RPD int   `yaml:"rpd,omitempty"`
	TPM int64 `yaml:"tpm,omitempty"`
	TPD int64 `yaml:"tpd,omitempty"`
}

// CapabilitiesConfig 是模态/特性声明。
type CapabilitiesConfig struct {
	Input    []string `yaml:"input"`
	Output   []string `yaml:"output"`
	Features []string `yaml:"features"`
}

// ModelDeclConfig 是一个静态模型声明（定价指针语义与内置一致：
// 显式 0 = 免费声明，省略 = 未登记）。
type ModelDeclConfig struct {
	ID            string   `yaml:"id"`
	ContextWindow int64    `yaml:"context_window,omitempty"`
	PriceIn       *float64 `yaml:"price_in,omitempty"`
	PriceOut      *float64 `yaml:"price_out,omitempty"`
}
