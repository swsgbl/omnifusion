// Package registry loads the built-in provider declarations (embedded
// YAML under providers/, per docs/04-架构设计 §2 工程结构) and
// instantiates provider.Provider adapters from them. Endpoint, auth
// and free-tier facts are verified against upstream sources — see the
// comments in each YAML file.
package registry

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/anthropic"
	"github.com/swsgbl/omnifusion/internal/provider/gemini"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
)

//go:embed providers/*.yaml
var embedded embed.FS

// 适配器种类（M1 仅 openai_compat；M3.2 起原生 Anthropic/Gemini 协议
// 适配器接入，语义翻译收在 internal/translate）。
const (
	KindOpenAICompat = "openai_compat"
	KindAnthropic    = "anthropic"
	KindGemini       = "gemini"
)

// ModelDecl is one statically declared model entry.
type ModelDecl struct {
	ID            string `yaml:"id"`
	ContextWindow int64  `yaml:"context_window,omitempty"`
}

// RateLimitsDecl declares the free-tier sliding-window quotas of a
// provider key (M2.3); 0/absent means unlimited. Facts come from each
// YAML's free_tier notes (verified during M1.3 research).
type RateLimitsDecl struct {
	RPM int   `yaml:"rpm,omitempty"` // requests / minute
	RPD int   `yaml:"rpd,omitempty"` // requests / day
	TPM int64 `yaml:"tpm,omitempty"` // tokens / minute
	TPD int64 `yaml:"tpd,omitempty"` // tokens / day
}

// CapabilityDecl mirrors provider.Capability in YAML form.
type CapabilityDecl struct {
	Input    []string `yaml:"input"`
	Output   []string `yaml:"output"`
	Features []string `yaml:"features"`
}

// Entry is one provider declaration in providers/*.yaml.
type Entry struct {
	ID          string `yaml:"id"`
	Kind        string `yaml:"kind"`
	DisplayName string `yaml:"display_name"`
	BaseURL     string `yaml:"base_url"`
	// Path overrides the adapter default (/chat/completions).
	Path      string `yaml:"path,omitempty"`
	AuthStyle string `yaml:"auth_style,omitempty"`
	// KeyEnv names the environment variable the key is read from when
	// not supplied through the keyring (M1.7).
	KeyEnv string `yaml:"key_env,omitempty"`
	// URLVars lists {placeholder} names that must be substituted into
	// BaseURL; VarsEnv maps each to an environment variable name.
	URLVars []string          `yaml:"url_vars,omitempty"`
	VarsEnv map[string]string `yaml:"vars_env,omitempty"`
	// OptionalKey marks providers that work without credentials
	// (local Ollama).
	OptionalKey  bool              `yaml:"optional_key,omitempty"`
	ExtraHeaders map[string]string `yaml:"extra_headers,omitempty"`
	ModelAliases map[string]string `yaml:"model_aliases,omitempty"`
	RateLimits   RateLimitsDecl    `yaml:"rate_limits,omitempty"`
	Capabilities CapabilityDecl    `yaml:"capabilities"`
	FreeTier     string            `yaml:"free_tier,omitempty"`
	Models       []ModelDecl       `yaml:"models"`
}

type file struct {
	Providers []Entry `yaml:"providers"`
}

// Credentials carries the material needed to instantiate one provider.
type Credentials struct {
	Key  string
	Vars map[string]string
}

// Load parses every embedded declaration file and returns the entries
// sorted by id.
func Load() ([]Entry, error) {
	dirEntries, err := fs.ReadDir(embedded, "providers")
	if err != nil {
		return nil, fmt.Errorf("registry: read embedded dir: %w", err)
	}
	var out []Entry
	for _, de := range dirEntries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".yaml") {
			continue
		}
		raw, err := embedded.ReadFile("providers/" + de.Name())
		if err != nil {
			return nil, fmt.Errorf("registry: read %s: %w", de.Name(), err)
		}
		var f file
		if err := yaml.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("registry: parse %s: %w", de.Name(), err)
		}
		for i := range f.Providers {
			e := &f.Providers[i]
			if e.ID == "" || e.BaseURL == "" || e.Kind == "" {
				return nil, fmt.Errorf("registry: %s: entry missing id/kind/base_url", de.Name())
			}
			out = append(out, *e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Build instantiates the adapter for one declaration. An empty key is
// rejected unless the entry sets optional_key; every declared url_var
// must be present in creds.Vars. Protocol semantics live in
// internal/translate; each kind below only picks the HTTP shell.
func Build(e Entry, creds Credentials) (provider.Provider, error) {
	if !e.OptionalKey && creds.Key == "" {
		return nil, fmt.Errorf("registry: provider %q requires an API key", e.ID)
	}
	baseURL, err := e.resolveBaseURL(creds)
	if err != nil {
		return nil, err
	}
	cap := provider.Capability{
		InputModalities:  e.Capabilities.Input,
		OutputModalities: e.Capabilities.Output,
		Features:         e.Capabilities.Features,
	}
	switch e.Kind {
	case KindOpenAICompat:
		return openai_compat.New(openai_compat.Spec{
			ProviderName: e.ID,
			BaseURL:      baseURL,
			Path:         e.Path,
			AuthStyle:    openai_compat.AuthStyle(e.AuthStyle),
			APIKey:       creds.Key,
			ExtraHeaders: e.ExtraHeaders,
			ModelAliases: e.ModelAliases,
			Cap:          cap,
		})
	case KindAnthropic:
		return anthropic.New(anthropic.Spec{
			ProviderName: e.ID,
			BaseURL:      baseURL,
			APIKey:       creds.Key,
			ExtraHeaders: e.ExtraHeaders,
			Cap:          cap,
		})
	case KindGemini:
		return gemini.New(gemini.Spec{
			ProviderName: e.ID,
			BaseURL:      baseURL,
			APIKey:       creds.Key,
			ExtraHeaders: e.ExtraHeaders,
			Cap:          cap,
		})
	default:
		return nil, fmt.Errorf("registry: provider %q has unsupported kind %q", e.ID, e.Kind)
	}
}

// resolveBaseURL substitutes declared url_vars into BaseURL.
func (e Entry) resolveBaseURL(creds Credentials) (string, error) {
	baseURL := e.BaseURL
	for _, v := range e.URLVars {
		val := creds.Vars[v]
		if val == "" {
			return "", fmt.Errorf("registry: provider %q needs url variable %q", e.ID, v)
		}
		baseURL = strings.ReplaceAll(baseURL, "{"+v+"}", val)
	}
	if strings.Contains(baseURL, "{") {
		return "", fmt.Errorf("registry: provider %q base_url has unsubstituted variables", e.ID)
	}
	return baseURL, nil
}

// StaticModels converts the declared model list into catalog entries.
// It backs the /v1/models surface when an adapter returns
// provider.ErrNotSupported, until live catalog sync lands (M6).
func (e Entry) StaticModels() []provider.ModelInfo {
	out := make([]provider.ModelInfo, 0, len(e.Models))
	for _, m := range e.Models {
		out = append(out, provider.ModelInfo{ID: m.ID, ContextWindow: m.ContextWindow})
	}
	return out
}
