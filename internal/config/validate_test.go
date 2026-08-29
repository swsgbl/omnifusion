// validate_test.go 覆盖 fusion/semantic/mlrouter/catalog 各段校验规则（M6.x）。
package config

import (
	"strings"
	"testing"
)

// fusionMembers 是合法的三成员扇出组（M6.1 测试基线）。
func fusionMembers() []ComboMemberConfig {
	return []ComboMemberConfig{
		{Provider: "a", Model: "model-a"},
		{Provider: "b", Model: "model-b"},
		{Provider: "c", Model: "model-c"},
	}
}

func TestValidateFusionAccepts(t *testing.T) {
	cfg := Default()
	cfg.Fusion = FusionConfig{Members: fusionMembers()} // quorum 缺省 2
	if err := cfg.Validate(); err != nil {
		t.Fatalf("合法 fusion 段被拒: %v", err)
	}
	cfg.Fusion.Judge = &ComboMemberConfig{Provider: "c", Model: "model-c"}
	cfg.Fusion.Quorum = 3
	if err := cfg.Validate(); err != nil {
		t.Fatalf("显式 judge/quorum 应合法: %v", err)
	}
}

func TestValidateFusionRejects(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*FusionConfig)
		want string
	}{
		{"单成员", func(f *FusionConfig) { f.Members = f.Members[:1] }, "至少 2 个成员"},
		{"成员缺 model", func(f *FusionConfig) {
			f.Members = []ComboMemberConfig{{Provider: "a"}, {Provider: "b", Model: "m-b"}}
		}, "均不能为空"},
		{"quorum 过小", func(f *FusionConfig) { f.Quorum = 1 }, "超出范围"},
		{"quorum 过大", func(f *FusionConfig) { f.Quorum = 4 }, "超出范围"},
		{"judge 缺字段", func(f *FusionConfig) { f.Judge = &ComboMemberConfig{Provider: "a"} }, "均不能为空"},
	}
	for _, tc := range cases {
		cfg := Default()
		cfg.Fusion = FusionConfig{Members: fusionMembers()}
		tc.mut(&cfg.Fusion)
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want 含 %q", tc.name, err, tc.want)
		}
	}
}

func TestValidateSemanticAccepts(t *testing.T) {
	cfg := Default() // 零值 semantic 段：不附加配置，合法
	if err := cfg.Validate(); err != nil {
		t.Fatalf("零值 semantic 段被拒: %v", err)
	}
	cfg.Semantic = SemanticConfig{SidecarURL: "http://127.0.0.1:8000/compress", Rate: 0.5}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("合法 sidecar 配置被拒: %v", err)
	}
	cfg.Semantic.Rate = 0 // rate 缺省合法（装配期取默认）
	if err := cfg.Validate(); err != nil {
		t.Fatalf("rate 缺省应合法: %v", err)
	}
}

func TestValidateSemanticRejects(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*SemanticConfig)
		want string
	}{
		{"rate 为负", func(s *SemanticConfig) { s.Rate = -0.1 }, "超出范围"},
		{"rate 超一", func(s *SemanticConfig) { s.Rate = 1.5 }, "超出范围"},
		{"sidecar 非 URL", func(s *SemanticConfig) { s.SidecarURL = "not-a-url" }, "不是合法"},
		{"sidecar 非 http", func(s *SemanticConfig) { s.SidecarURL = "ftp://x/y" }, "不是合法"},
	}
	for _, tc := range cases {
		cfg := Default()
		tc.mut(&cfg.Semantic)
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want 含 %q", tc.name, err, tc.want)
		}
	}
}

func TestValidateMLRouterAccepts(t *testing.T) {
	cfg := Default() // 零值 mlrouter 段：未启用，合法
	if err := cfg.Validate(); err != nil {
		t.Fatalf("零值 mlrouter 段被拒: %v", err)
	}
	cfg.MLRouter = MLRouterConfig{
		Weak:   &ComboMemberConfig{Provider: "a", Model: "m-small"},
		Strong: &ComboMemberConfig{Provider: "b", Model: "m-big"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("合法 mlrouter 段被拒: %v", err)
	}
	cfg.MLRouter.Threshold = 0.7 // 显式阈值合法
	if err := cfg.Validate(); err != nil {
		t.Fatalf("显式 threshold 应合法: %v", err)
	}
}

func TestValidateMLRouterRejects(t *testing.T) {
	valid := func() MLRouterConfig {
		return MLRouterConfig{
			Weak:   &ComboMemberConfig{Provider: "a", Model: "m-small"},
			Strong: &ComboMemberConfig{Provider: "b", Model: "m-big"},
		}
	}
	cases := []struct {
		name string
		mut  func(*MLRouterConfig)
		want string
	}{
		{"只配 weak", func(m *MLRouterConfig) { m.Weak = nil }, "必须同时配置"},
		{"只配 strong", func(m *MLRouterConfig) { m.Strong = nil }, "必须同时配置"},
		{"weak 缺 model", func(m *MLRouterConfig) { m.Weak.Model = "" }, "均不能为空"},
		{"strong 缺 provider", func(m *MLRouterConfig) { m.Strong.Provider = "" }, "均不能为空"},
		{"threshold 超一", func(m *MLRouterConfig) { m.Threshold = 1.2 }, "超出范围"},
		{"threshold 为负", func(m *MLRouterConfig) { m.Threshold = -0.1 }, "超出范围"},
	}
	for _, tc := range cases {
		cfg := Default()
		cfg.MLRouter = valid()
		tc.mut(&cfg.MLRouter)
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want 含 %q", tc.name, err, tc.want)
		}
	}
}

func TestValidateCatalog(t *testing.T) {
	// 双空 = 未启用，放行（Default 零值段）。
	if err := Default().Validate(); err != nil {
		t.Fatalf("零值 catalog 段被拒: %v", err)
	}
	cfg := Default()
	cfg.Catalog = CatalogConfig{
		FeedURL:    "http://feeds.example/catalog.json",
		FeedPubkey: strings.Repeat("ab", 32),
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("合法 catalog 段被拒: %v", err)
	}

	cases := []struct {
		name string
		mut  func(*CatalogConfig)
		want string
	}{
		{"只配 url", func(c *CatalogConfig) { c.FeedPubkey = "" }, "configured together"},
		{"只配 pubkey", func(c *CatalogConfig) { c.FeedURL = "" }, "configured together"},
		{"非 http scheme", func(c *CatalogConfig) { c.FeedURL = "ftp://x/y" }, "valid http(s) URL"},
		{"无 host", func(c *CatalogConfig) { c.FeedURL = "http:///path" }, "valid http(s) URL"},
		{"pubkey 非 hex", func(c *CatalogConfig) { c.FeedPubkey = strings.Repeat("zz", 32) }, "64 hex chars"},
		{"pubkey 过短", func(c *CatalogConfig) { c.FeedPubkey = strings.Repeat("a", 63) }, "64 hex chars"},
	}
	for _, tc := range cases {
		cfg := Default()
		cfg.Catalog = CatalogConfig{
			FeedURL:    "http://feeds.example/catalog.json",
			FeedPubkey: strings.Repeat("ab", 32),
		}
		tc.mut(&cfg.Catalog)
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want 含 %q", tc.name, err, tc.want)
		}
	}
}
