package openai_compat

import (
	"context"
	"encoding/json"
	"github.com/swsgbl/omnifusion/internal/core/schema"
	"net/http"
	"strings"
	"testing"
)

func baseSpec() Spec {
	return Spec{
		ProviderName: "mock",
		BaseURL:      "https://api.mock.test/v1",
		APIKey:       "sk-test",
	}
}

func sampleRequest(model string) *schema.UnifiedRequest {
	return &schema.UnifiedRequest{
		Model: model,
		Messages: []schema.Message{
			{Role: "user", Content: schema.NewTextContent("hi")},
		},
	}
}

func TestTranslateDefaults(t *testing.T) {
	a, err := New(baseSpec())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	call, err := a.Translate(context.Background(), sampleRequest("gpt-x"))
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if want := "https://api.mock.test/v1/chat/completions"; call.URL != want {
		t.Errorf("URL = %q, want %q", call.URL, want)
	}
	if got := call.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("Authorization = %q", got)
	}
	if got := call.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if call.Method != http.MethodPost {
		t.Errorf("Method = %q", call.Method)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(call.Body, &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if got := strings.Trim(string(body["model"]), `"`); got != "gpt-x" {
		t.Errorf("body model = %q", got)
	}
	if call.Stream {
		t.Error("Stream should be false")
	}
	if call.Original == nil {
		t.Error("Original must reference the inbound request")
	}
}

func TestTranslateDoesNotMutateCaller(t *testing.T) {
	a, err := New(Spec{
		ProviderName: "mock",
		BaseURL:      "https://api.mock.test/v1",
		APIKey:       "sk-test",
		ModelAliases: map[string]string{"alias": "real-model"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := sampleRequest("alias")
	if _, err := a.Translate(context.Background(), req); err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if req.Model != "alias" {
		t.Errorf("caller request mutated: model = %q", req.Model)
	}
}

func TestTranslateModelAlias(t *testing.T) {
	a, err := New(Spec{
		ProviderName: "mock",
		BaseURL:      "https://api.mock.test/v1",
		APIKey:       "sk-test",
		ModelAliases: map[string]string{"fast": "llama-fast-8b"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	call, err := a.Translate(context.Background(), sampleRequest("fast"))
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if call.Model != "llama-fast-8b" {
		t.Errorf("call.Model = %q", call.Model)
	}
	if !strings.Contains(string(call.Body), `"llama-fast-8b"`) {
		t.Errorf("body should carry aliased model: %s", call.Body)
	}
}

func TestTranslateAuthHeaderStyle(t *testing.T) {
	a, err := New(Spec{
		ProviderName: "mock",
		BaseURL:      "https://api.mock.test/v1",
		AuthStyle:    AuthHeader,
		APIKeyHeader: "X-Api-Key",
		APIKey:       "secret",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	call, err := a.Translate(context.Background(), sampleRequest("m"))
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if got := call.Header.Get("X-Api-Key"); got != "secret" {
		t.Errorf("X-Api-Key = %q", got)
	}
	if got := call.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization should be empty, got %q", got)
	}
}

func TestTranslateAuthQueryStyle(t *testing.T) {
	a, err := New(Spec{
		ProviderName:  "mock",
		BaseURL:       "https://api.mock.test/v1",
		AuthStyle:     AuthQuery,
		APIQueryParam: "api_key",
		APIKey:        "secret",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	call, err := a.Translate(context.Background(), sampleRequest("m"))
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if want := "https://api.mock.test/v1/chat/completions?api_key=secret"; call.URL != want {
		t.Errorf("URL = %q, want %q", call.URL, want)
	}
	if got := call.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization should be empty, got %q", got)
	}
}

func TestTranslateExtraHeadersAndPath(t *testing.T) {
	a, err := New(Spec{
		ProviderName: "mock",
		BaseURL:      "https://api.mock.test",
		Path:         "/openai/chat",
		APIKey:       "sk-test",
		ExtraHeaders: map[string]string{
			"HTTP-Referer": "https://omnifusion.local",
			"X-Title":      "OmniFusion",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	call, err := a.Translate(context.Background(), sampleRequest("m"))
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if want := "https://api.mock.test/openai/chat"; call.URL != want {
		t.Errorf("URL = %q, want %q", call.URL, want)
	}
	if got := call.Header.Get("HTTP-Referer"); got != "https://omnifusion.local" {
		t.Errorf("HTTP-Referer = %q", got)
	}
	if got := call.Header.Get("X-Title"); got != "OmniFusion" {
		t.Errorf("X-Title = %q", got)
	}
}

func TestTranslateMaxTokensPolicies(t *testing.T) {
	ctx := context.Background()

	t.Run("required fills default", func(t *testing.T) {
		a, err := New(Spec{
			ProviderName:   "mock",
			BaseURL:        "https://api.mock.test/v1",
			APIKey:         "sk-test",
			MaxTokens:      MaxTokensRequired,
			DefaultMaxToks: 512,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		call, err := a.Translate(ctx, sampleRequest("m"))
		if err != nil {
			t.Fatalf("Translate: %v", err)
		}
		if !strings.Contains(string(call.Body), `"max_tokens":512`) {
			t.Errorf("body should carry max_tokens=512: %s", call.Body)
		}
	})

	t.Run("omit strips field", func(t *testing.T) {
		a, err := New(Spec{
			ProviderName: "mock",
			BaseURL:      "https://api.mock.test/v1",
			APIKey:       "sk-test",
			MaxTokens:    MaxTokensOmit,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		req := sampleRequest("m")
		v := 100
		req.MaxTokens = &v
		call, err := a.Translate(ctx, req)
		if err != nil {
			t.Fatalf("Translate: %v", err)
		}
		if strings.Contains(string(call.Body), "max_tokens") {
			t.Errorf("body should omit max_tokens: %s", call.Body)
		}
		if req.MaxTokens == nil || *req.MaxTokens != 100 {
			t.Error("caller request mutated")
		}
	})
}
