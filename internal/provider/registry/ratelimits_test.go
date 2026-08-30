package registry

import "testing"

// TestRateLimitsDeclared 断言免费层事实已从 YAML 正确解析进
// RateLimitsDecl（registry 是 rate_limits 的唯一事实来源）。
func TestRateLimitsDeclared(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	byID := map[string]Entry{}
	for _, e := range entries {
		byID[e.ID] = e
	}

	want := map[string]RateLimitsDecl{
		"groq":       {RPM: 30, RPD: 14400},
		"openrouter": {RPM: 20, RPD: 200},
		"cerebras":   {RPM: 5, TPM: 30000, TPD: 1000000},
		"nvidia":     {RPM: 40},
		"cloudflare": {RPD: 150},
	}
	for id, w := range want {
		if got := byID[id].RateLimits; got != w {
			t.Errorf("%s: RateLimits = %+v, want %+v", id, got, w)
		}
	}

	// 窗口制不适用的 provider 必须不声明：
	// gemini/huggingface 按模型、按月额度（非滑动窗口），anthropic 无
	// 免费层（BYOK 付费），ollama 本地无限制。
	for _, id := range []string{"anthropic", "gemini", "huggingface", "ollama"} {
		if rl := byID[id].RateLimits; rl != (RateLimitsDecl{}) {
			t.Errorf("%s: RateLimits = %+v, want zero (non-windowed quota model)", id, rl)
		}
	}
}
