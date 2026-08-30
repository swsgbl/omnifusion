// combos_test.go 覆盖组合段：YAML 加载与成员校验。
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCombosYAML(t *testing.T) {
	content := `
server:
  host: 127.0.0.1
  port: 20130
combos:
  routing:
    free-tier:
      members:
        - provider: groq
          model: llama-3.3-70b-versatile
        - provider: openrouter
          model: meta-llama/llama-3.3-70b-instruct:free
      compression: aggressive
    plain:
      members:
        - provider: groq
          model: llama-3.3-70b-versatile
  compression:
    lite: [dedup]
    aggressive: [dedup, toolfilter, caveman]
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rc := cfg.Combos.Routing["free-tier"]
	if len(rc.Members) != 2 || rc.Members[0].Provider != "groq" ||
		rc.Members[1].Model != "meta-llama/llama-3.3-70b-instruct:free" {
		t.Errorf("free-tier members = %+v", rc.Members)
	}
	if rc.Compression != "aggressive" {
		t.Errorf("binding = %q, want aggressive", rc.Compression)
	}
	stages := cfg.Combos.Compression["aggressive"]
	if len(stages) != 3 || stages[0] != "dedup" || stages[2] != "caveman" {
		t.Errorf("aggressive stages = %v", stages)
	}
	if cfg.Combos.Routing["plain"].Compression != "" {
		t.Error("plain 组合应无压缩绑定")
	}
}

func TestCombosValidation(t *testing.T) {
	base := func() *Config {
		c := Default()
		c.Combos = CombosConfig{
			Routing: map[string]RoutingComboConfig{
				"ok": {
					Members:     []ComboMemberConfig{{Provider: "a", Model: "m"}},
					Compression: "std",
				},
			},
			Compression: map[string][]string{"std": {"dedup"}},
		}
		return c
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("合法组合不应报错: %v", err)
	}

	noMembers := base()
	noMembers.Combos.Routing["bad"] = RoutingComboConfig{}
	if err := noMembers.Validate(); err == nil {
		t.Error("空成员应报错")
	}

	blankField := base()
	blankField.Combos.Routing["bad"] = RoutingComboConfig{
		Members: []ComboMemberConfig{{Provider: "", Model: "m"}},
	}
	if err := blankField.Validate(); err == nil {
		t.Error("成员字段缺失应报错")
	}

	badBinding := base()
	badBinding.Combos.Routing["bad"] = RoutingComboConfig{
		Members:     []ComboMemberConfig{{Provider: "a", Model: "m"}},
		Compression: "ghost",
	}
	if err := badBinding.Validate(); err == nil {
		t.Error("绑定未定义压缩组合应报错")
	}
}
