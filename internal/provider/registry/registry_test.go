package registry

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

var expectedIDs = []string{
	"anthropic", "cerebras", "cloudflare", "gemini", "groq",
	"huggingface", "nvidia", "ollama", "openrouter",
}

// expectedKinds 声明非 openai_compat 的原生适配器（M3.2）。
var expectedKinds = map[string]string{
	"anthropic": KindAnthropic,
	"gemini":    KindGemini,
}

func TestLoadBuiltins(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var ids []string
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	if strings.Join(ids, ",") != strings.Join(expectedIDs, ",") {
		t.Errorf("ids = %v, want %v", ids, expectedIDs)
	}
	for _, e := range entries {
		wantKind, native := expectedKinds[e.ID]
		if !native {
			wantKind = KindOpenAICompat
		}
		if e.Kind != wantKind {
			t.Errorf("%s: kind = %q, want %q", e.ID, e.Kind, wantKind)
		}
		if e.DisplayName == "" {
			t.Errorf("%s: missing display_name", e.ID)
		}
	}
}

func TestBuildAllWithDummyCreds(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, e := range entries {
		creds := Credentials{Key: "dummy-key", Vars: map[string]string{"account_id": "acct123"}}
		if e.OptionalKey {
			creds.Key = ""
		}
		p, err := Build(e, creds)
		if err != nil {
			t.Errorf("Build(%s): %v", e.ID, err)
			continue
		}
		if p.Name() != e.ID {
			t.Errorf("%s: Name() = %q", e.ID, p.Name())
		}
		if p.HTTPClient() == nil {
			t.Errorf("%s: nil HTTP client", e.ID)
		}
	}
}

func TestBuildRejectsMissingKey(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, e := range entries {
		if e.OptionalKey {
			continue
		}
		if _, err := Build(e, Credentials{}); err == nil {
			t.Errorf("Build(%s) with empty key should fail", e.ID)
		}
	}
}

func TestBuildCloudflareURLSubstitution(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var e Entry
	found := false
	for _, cand := range entries {
		if cand.ID == "cloudflare" {
			e, found = cand, true
		}
	}
	if !found {
		t.Fatal("cloudflare entry not found")
	}

	if _, err := Build(e, Credentials{Key: "k"}); err == nil {
		t.Error("Build without account_id should fail")
	}

	p, err := Build(e, Credentials{Key: "k", Vars: map[string]string{"account_id": "acct123"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	call, err := p.Translate(context.Background(), &schema.UnifiedRequest{
		Model:    "@cf/meta/llama-3.3-70b-instruct-fp8-fast",
		Messages: []schema.Message{{Role: "user", Content: schema.NewTextContent("hi")}},
	})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	want := "https://api.cloudflare.com/client/v4/accounts/acct123/ai/v1/chat/completions"
	if call.URL != want {
		t.Errorf("URL = %q, want %q", call.URL, want)
	}
}

func TestBuildOpenRouterHeaders(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var e Entry
	for _, cand := range entries {
		if cand.ID == "openrouter" {
			e = cand
		}
	}
	p, err := Build(e, Credentials{Key: "sk-or-test"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	call, err := p.Translate(context.Background(), &schema.UnifiedRequest{
		Model:    "openrouter/auto",
		Messages: []schema.Message{{Role: "user", Content: schema.NewTextContent("hi")}},
	})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if got := call.Header.Get("Authorization"); got != "Bearer sk-or-test" {
		t.Errorf("Authorization = %q", got)
	}
	if got := call.Header.Get("HTTP-Referer"); got == "" {
		t.Error("HTTP-Referer missing")
	}
	if got := call.Header.Get("X-Title"); got != "OmniFusion" {
		t.Errorf("X-Title = %q", got)
	}
}

func TestStaticModels(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	counts := map[string]int{}
	for _, e := range entries {
		counts[e.ID] = len(e.StaticModels())
	}
	if counts["groq"] == 0 {
		t.Error("groq should declare static models")
	}
	if counts["ollama"] != 0 {
		t.Errorf("ollama static models = %d, want 0 (dynamic local catalog)", counts["ollama"])
	}
}

func TestCapabilitiesPropagate(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, e := range entries {
		p, err := Build(e, Credentials{Key: "k", Vars: map[string]string{"account_id": "a"}})
		if err != nil {
			t.Fatalf("Build(%s): %v", e.ID, err)
		}
		caps := p.Capabilities()
		if len(caps.InputModalities) == 0 {
			t.Errorf("%s: no input modalities declared", e.ID)
		}
		if !caps.SupportsInput("text") {
			t.Errorf("%s: text input not declared", e.ID)
		}
		if !caps.HasFeature("tools") {
			t.Errorf("%s: tools feature not declared", e.ID)
		}
		// Interface assignability check against the frozen surface.
		var _ = p
	}
}
