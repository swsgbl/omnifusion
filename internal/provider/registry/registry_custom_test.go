package registry

import "testing"

// TestLoadWithMergeVerify 用户自定义 provider 合并：同 id 覆盖内置
//（自定义声明整体替换——改 base_url 后原 models 不残留），新 id 追加，
// 非法条目（缺 base_url / price 半配对）拒绝。
func TestLoadWithMergeVerify(t *testing.T) {
	builtin, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	override := Entry{ID: "groq", Kind: KindOpenAICompat, BaseURL: "https://example.internal/v1",
		DisplayName: "Groq (custom)", Capabilities: CapabilityDecl{Input: []string{"text"}}}
	custom := Entry{ID: "myproxy", Kind: KindOpenAICompat, BaseURL: "https://proxy.example/v1",
		KeyEnv: "MYPROXY_API_KEY", Capabilities: CapabilityDecl{Input: []string{"text"}},
		Models: []ModelDecl{{ID: "m1"}}}
	merged, err := LoadWith([]Entry{override, custom})
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != len(builtin)+1 {
		t.Fatalf("merged = %d, want %d+1", len(merged), len(builtin))
	}
	for _, e := range merged {
		if e.ID == "groq" {
			if e.BaseURL != override.BaseURL || e.DisplayName != override.DisplayName {
				t.Errorf("groq override not applied: %+v", e)
			}
			if len(e.Models) != 0 {
				t.Errorf("override should fully replace builtin entry, got %d models", len(e.Models))
			}
		}
		if e.ID == "myproxy" && e.KeyEnv != "MYPROXY_API_KEY" {
			t.Errorf("custom entry wrong: %+v", e)
		}
	}
	if _, err := LoadWith([]Entry{{ID: "x", Kind: KindOpenAICompat}}); err == nil {
		t.Error("missing base_url should be rejected")
	}
	p := 1.0
	if _, err := LoadWith([]Entry{{ID: "x", Kind: KindOpenAICompat, BaseURL: "https://x",
		Models: []ModelDecl{{ID: "m", PriceIn: &p}}}}); err == nil {
		t.Error("half price pair should be rejected")
	}
}
