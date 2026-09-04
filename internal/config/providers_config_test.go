package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestProviderConfigRoundtripGuard 守卫 config.ProviderConfig 与
// registry.Entry 的字段同构性：全字段样例经 config → yaml → registry
// Entry 反序列化后逐字段一致（两套类型分处两个叶子包，靠本测试防
// 字段漂移——漂移会让自定义 provider 静默丢字段）。
func TestProviderConfigRoundtripGuard(t *testing.T) {
	pin, pout := 0.0, 1.5
	pc := ProviderConfig{
		ID: "guard", Kind: "openai_compat", DisplayName: "Guard",
		BaseURL: "https://guard.example/v1", Path: "/chat",
		AuthStyle: "bearer", KeyEnv: "GUARD_API_KEY", SignupURL: "https://guard.example/signup",
		URLVars:  []string{"region"},
		VarsEnv: map[string]string{"region": "GUARD_REGION"},
		OptionalKey: true,
		ExtraHeaders: map[string]string{"X-Probe": "1"},
		ModelAliases: map[string]string{"alias": "real"},
		RateLimits: RateLimitsConfig{RPM: 10, RPD: 100, TPM: 4000, TPD: 40000},
		Capabilities: CapabilitiesConfig{Input: []string{"text"}, Output: []string{"text"}, Features: []string{"tools"}},
		FreeTier: "guard tier",
		Models: []ModelDeclConfig{{ID: "m", ContextWindow: 8192, PriceIn: &pin, PriceOut: &pout}},
	}
	raw, err := yaml.Marshal(pc)
	if err != nil {
		t.Fatal(err)
	}
	// registry.Entry 的镜像结构（字段与 tag 逐一同构；漂移即失败）。
	var got struct {
		ID          string   `yaml:"id"`
		Kind        string   `yaml:"kind"`
		DisplayName string   `yaml:"display_name"`
		BaseURL     string   `yaml:"base_url"`
		Path        string   `yaml:"path,omitempty"`
		AuthStyle   string   `yaml:"auth_style,omitempty"`
		KeyEnv      string   `yaml:"key_env,omitempty"`
		SignupURL   string   `yaml:"signup_url,omitempty"`
		URLVars     []string `yaml:"url_vars,omitempty"`
		VarsEnv     map[string]string `yaml:"vars_env,omitempty"`
		OptionalKey  bool   `yaml:"optional_key,omitempty"`
		ExtraHeaders map[string]string `yaml:"extra_headers,omitempty"`
		ModelAliases map[string]string `yaml:"model_aliases,omitempty"`
		RateLimits   struct {
			RPM int   `yaml:"rpm,omitempty"`
			RPD int   `yaml:"rpd,omitempty"`
			TPM int64 `yaml:"tpm,omitempty"`
			TPD int64 `yaml:"tpd,omitempty"`
		} `yaml:"rate_limits,omitempty"`
		Capabilities struct {
			Input    []string `yaml:"input"`
			Output   []string `yaml:"output"`
			Features []string `yaml:"features"`
		} `yaml:"capabilities"`
		FreeTier string `yaml:"free_tier,omitempty"`
		Models   []struct {
			ID            string   `yaml:"id"`
			ContextWindow int64    `yaml:"context_window,omitempty"`
			PriceIn       *float64 `yaml:"price_in,omitempty"`
			PriceOut      *float64 `yaml:"price_out,omitempty"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	check := func(name string, ok bool) {
		if !ok {
			t.Errorf("field %s lost in roundtrip; yaml:\n%s", name, raw)
		}
	}
	check("id", got.ID == "guard")
	check("kind", got.Kind == "openai_compat")
	check("display_name", got.DisplayName == "Guard")
	check("base_url", got.BaseURL == "https://guard.example/v1")
	check("path", got.Path == "/chat")
	check("auth_style", got.AuthStyle == "bearer")
	check("key_env", got.KeyEnv == "GUARD_API_KEY")
	check("signup_url", got.SignupURL == "https://guard.example/signup")
	check("url_vars", len(got.URLVars) == 1 && got.URLVars[0] == "region")
	check("vars_env", len(got.VarsEnv) == 1 && got.VarsEnv["region"] == "GUARD_REGION")
	check("optional_key", got.OptionalKey)
	check("extra_headers", len(got.ExtraHeaders) == 1)
	check("model_aliases", len(got.ModelAliases) == 1)
	check("rate_limits", got.RateLimits.RPM == 10 && got.RateLimits.RPD == 100 && got.RateLimits.TPM == 4000 && got.RateLimits.TPD == 40000)
	check("capabilities", len(got.Capabilities.Input) == 1 && len(got.Capabilities.Features) == 1)
	check("free_tier", got.FreeTier == "guard tier")
	check("models", len(got.Models) == 1 && got.Models[0].ContextWindow == 8192 &&
		got.Models[0].PriceIn != nil && *got.Models[0].PriceIn == 0 && *got.Models[0].PriceOut == 1.5)
}
