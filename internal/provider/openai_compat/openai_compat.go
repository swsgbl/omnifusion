// Package openai_compat implements the declarative adapter that covers
// the large majority of OpenAI-shaped providers (
// §4.1). Behavioural differences are expressed as Spec data — headers,
// auth style, path, model aliases, max_tokens policy — not as forked
// code paths.
package openai_compat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
)

// DefaultPath is the OpenAI chat completions endpoint path used when a
// Spec does not override it.
const DefaultPath = "/chat/completions"

// DefaultTimeout bounds a single upstream request. Providers tune it
// through Spec.Timeout.
const DefaultTimeout = 120 * time.Second

// dialTimeout bounds TCP/TLS establishment. Unreachable providers must
// fail fast so the fallback chain is not stalled by a slow SYN timeout
// (observed: 21s default dial on a filtered upstream).
const dialTimeout = 10 * time.Second

// maxErrorBody bounds how much of a failing upstream body is retained
// for logging.
const maxErrorBody = 1 << 20 // 1 MiB

// AuthStyle selects how the provider expects its API key.
type AuthStyle string

const (
	// AuthBearer sends "Authorization: Bearer <key>" (default).
	AuthBearer AuthStyle = "bearer"
	// AuthHeader sends the key in a provider-specific header named by
	// Spec.APIKeyHeader.
	AuthHeader AuthStyle = "header"
	// AuthQuery appends the key as a query parameter named by
	// Spec.APIQueryParam.
	AuthQuery AuthStyle = "query"
)

// MaxTokensPolicy captures the per-provider divergence around
// max_tokens ().
type MaxTokensPolicy string

const (
	// MaxTokensPass forwards whatever the caller set (default).
	MaxTokensPass MaxTokensPolicy = "pass"
	// MaxTokensRequired forces a value: the caller's, or
	// Spec.DefaultMaxTokens when unset.
	MaxTokensRequired MaxTokensPolicy = "required"
	// MaxTokensOmit strips the field before sending.
	MaxTokensOmit MaxTokensPolicy = "omit"
)

// Spec is the declarative description of one OpenAI-compatible
// provider. The registry builds it from YAML (); tests build it
// directly.
type Spec struct {
	ProviderName string
	BaseURL      string
	// Path overrides DefaultPath when set.
	Path string

	AuthStyle      AuthStyle
	APIKey         string
	APIKeyHeader   string // for AuthHeader
	APIQueryParam  string // for AuthQuery
	ExtraHeaders   map[string]string
	ModelAliases   map[string]string
	MaxTokens      MaxTokensPolicy
	DefaultMaxToks int
	Cap            provider.Capability
	// Timeout tunes this provider's dedicated HTTP client.
	Timeout time.Duration
}

func (s Spec) validate() error {
	if s.ProviderName == "" {
		return fmt.Errorf("openai_compat: spec missing provider name")
	}
	if s.BaseURL == "" {
		return fmt.Errorf("openai_compat: spec %q missing base_url", s.ProviderName)
	}
	switch s.AuthStyle {
	case "", AuthBearer:
	case AuthHeader:
		if s.APIKeyHeader == "" {
			return fmt.Errorf("openai_compat: %q auth_style=header needs api_key_header", s.ProviderName)
		}
	case AuthQuery:
		if s.APIQueryParam == "" {
			return fmt.Errorf("openai_compat: %q auth_style=query needs api_query_param", s.ProviderName)
		}
	default:
		return fmt.Errorf("openai_compat: %q unknown auth_style %q", s.ProviderName, s.AuthStyle)
	}
	switch s.MaxTokens {
	case "", MaxTokensPass, MaxTokensRequired, MaxTokensOmit:
	default:
		return fmt.Errorf("openai_compat: %q unknown max_tokens policy %q", s.ProviderName, s.MaxTokens)
	}
	return nil
}

// Adapter is the provider.Provider implementation driven by a Spec.
type Adapter struct {
	spec         Spec
	client       *http.Client
	streamClient *http.Client
}

// New builds an Adapter from a validated Spec.
func New(spec Spec) (*Adapter, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	// One dedicated client per provider (isolation lesson): connection
	// pool and timeouts are isolated. The dial
	// timeout keeps unreachable upstreams from stalling the
	// fallback chain.
	// Proxy honors HTTPS_PROXY/HTTP_PROXY/NO_PROXY ( L2
	// tiered proxy, global tier): region-blocked upstreams such as
	// Groq (403 on CN/HK egress before auth) only work through an
	// env-configured proxy; without it the request goes direct and
	// the fallback chain handles the failure. Per-provider proxy
	// tiers land with the L2 proxy.go work (+).
	transport := func() *http.Transport {
		return &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: dialTimeout}).DialContext,
			TLSHandshakeTimeout:   dialTimeout,
			ResponseHeaderTimeout: timeout,
		}
	}
	return &Adapter{
		spec:   spec,
		client: &http.Client{Timeout: timeout, Transport: transport()},
		// Streaming needs a client without a global Timeout: SSE bodies
		// legitimately outlive any request-level deadline. Header wait
		// stays bounded by ResponseHeaderTimeout; the overall lifetime
		// is bounded by the caller's context ( item 4).
		streamClient: &http.Client{Transport: transport()},
	}, nil
}

// Name implements provider.Provider.
func (a *Adapter) Name() string { return a.spec.ProviderName }

// Capabilities implements provider.Provider.
func (a *Adapter) Capabilities() provider.Capability { return a.spec.Cap }

// HTTPClient implements provider.Provider.
func (a *Adapter) HTTPClient() *http.Client { return a.client }

// ListModels 的实现在 models.go（实时目录拉取）。

// Translate implements provider.Provider: it rewrites the model id
// through the alias table, applies the max_tokens policy, serializes
// the unified body (extra fields included), and attaches auth plus any
// declared extra headers.
func (a *Adapter) Translate(ctx context.Context, req *schema.UnifiedRequest) (*provider.ProviderCall, error) {
	if req == nil {
		return nil, fmt.Errorf("openai_compat: %q nil request", a.spec.ProviderName)
	}
	out := *req // shallow copy: Translate must not mutate the caller's request

	if alias, ok := a.spec.ModelAliases[out.Model]; ok {
		out.Model = alias
	}
	switch a.spec.MaxTokens {
	case MaxTokensRequired:
		if out.MaxTokens == nil && a.spec.DefaultMaxToks > 0 {
			v := a.spec.DefaultMaxToks
			out.MaxTokens = &v
		}
	case MaxTokensOmit:
		out.MaxTokens = nil
	}

	body, err := json.Marshal(&out)
	if err != nil {
		return nil, fmt.Errorf("openai_compat: %q marshal: %w", a.spec.ProviderName, err)
	}

	url := strings.TrimRight(a.spec.BaseURL, "/") + a.path()
	if a.spec.AuthStyle == AuthQuery && a.spec.APIKey != "" {
		sep := "?"
		if strings.Contains(url, "?") {
			sep = "&"
		}
		url += sep + a.spec.APIQueryParam + "=" + a.spec.APIKey
	}

	header := make(http.Header, len(a.spec.ExtraHeaders)+2)
	header.Set("Content-Type", "application/json")
	header.Set("Accept", "application/json")
	switch a.spec.AuthStyle {
	case AuthHeader:
		if a.spec.APIKey != "" {
			header.Set(a.spec.APIKeyHeader, a.spec.APIKey)
		}
	case AuthQuery:
		// key already embedded in the URL above
	case "", AuthBearer:
		if a.spec.APIKey != "" {
			header.Set("Authorization", "Bearer "+a.spec.APIKey)
		}
	}
	for k, v := range a.spec.ExtraHeaders {
		header.Set(k, v)
	}

	return &provider.ProviderCall{
		Method:   http.MethodPost,
		URL:      url,
		Header:   header,
		Body:     body,
		Stream:   out.Stream,
		Model:    out.Model,
		Original: req,
	}, nil
}

// Parse implements provider.Provider for non-streaming responses:
// non-2xx becomes a typed UpstreamError; 2xx bodies decode into the
// unified Response with the provider name stamped for observability.
// The call's Original request stays available for streaming ()
// and reconciliation logic.
func (a *Adapter) Parse(ctx context.Context, call *provider.ProviderCall, resp *http.Response) (*schema.Response, error) {
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("openai_compat: %q nil upstream response", a.spec.ProviderName)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		if err != nil {
			return nil, fmt.Errorf("openai_compat: %q read error body: %w", a.spec.ProviderName, err)
		}
		return nil, &provider.UpstreamError{
			Provider: a.spec.ProviderName,
			Status:   resp.StatusCode,
			Body:     bytes.TrimSpace(body),
		}
	}

	parsed, err := schema.NewResponseFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai_compat: %q decode: %w", a.spec.ProviderName, err)
	}
	parsed.ProviderName = a.spec.ProviderName
	if call != nil && parsed.Model == "" {
		parsed.Model = call.Model
	}
	return parsed, nil
}

func (a *Adapter) path() string {
	if a.spec.Path != "" {
		return a.spec.Path
	}
	return DefaultPath
}
