// Package gemini 是 Google Gemini generateContent 协议的原生上游适配
// 器（M3.2）：Translate 调 translate.ToGeminiUpstreamRequest，Parse 调
// FromGeminiUpstreamResponse——协议语义收在 internal/translate 纯函数对
// 里，本包只做 HTTP 壳。model 在 URL 路径里（非流式 :generateContent、
// 流式 :streamGenerateContent?alt=sse），鉴权走 x-goog-api-key 头。
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/translate"
)

// DefaultBaseURL 是 Google AI 的 generativelanguage 端点。
const DefaultBaseURL = "https://generativelanguage.googleapis.com"

// DefaultTimeout bounds a single non-streaming upstream request.
const DefaultTimeout = 120 * time.Second

// dialTimeout keeps unreachable upstreams from stalling the fallback
// chain (same rationale as openai_compat).
const dialTimeout = 10 * time.Second

// maxErrorBody bounds how much of a failing upstream body is retained.
const maxErrorBody = 1 << 20 // 1 MiB

// Spec declares one Gemini-shaped upstream.
type Spec struct {
	ProviderName string
	BaseURL      string // empty → DefaultBaseURL
	APIKey       string
	ExtraHeaders map[string]string
	Cap          provider.Capability
	// Timeout tunes this provider's dedicated HTTP client.
	Timeout time.Duration
}

func (s Spec) validate() error {
	if s.ProviderName == "" {
		return fmt.Errorf("gemini: spec missing provider name")
	}
	if s.APIKey == "" {
		return fmt.Errorf("gemini: spec %q missing api key", s.ProviderName)
	}
	return nil
}

// Adapter is the provider.Provider implementation for Gemini.
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
	if spec.BaseURL == "" {
		spec.BaseURL = DefaultBaseURL
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
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
		// SSE bodies legitimately outlive any request-level deadline
		// (same contract as openai_compat).
		streamClient: &http.Client{Transport: transport()},
	}, nil
}

// Name implements provider.Provider.
func (a *Adapter) Name() string { return a.spec.ProviderName }

// Capabilities implements provider.Provider.
func (a *Adapter) Capabilities() provider.Capability { return a.spec.Cap }

// HTTPClient implements provider.Provider.
func (a *Adapter) HTTPClient() *http.Client { return a.client }

// ListModels implements provider.Provider. The static registry list
// serves M3; live catalog sync lands M6.
func (a *Adapter) ListModels(ctx context.Context) ([]provider.ModelInfo, error) {
	return nil, provider.ErrNotSupported
}

// actionURL 拼接端点：非流式 :generateContent、流式走
// :streamGenerateContent?alt=sse（Google SSE 形）。
func (a *Adapter) actionURL(model string, stream bool) string {
	action := "generateContent"
	if stream {
		action = "streamGenerateContent?alt=sse"
	}
	return strings.TrimRight(a.spec.BaseURL, "/") +
		"/v1beta/models/" + url.PathEscape(model) + ":" + action
}

// Translate implements provider.Provider: IR → Gemini wire（model 进
// URL 路径，请求体不携带）。
func (a *Adapter) Translate(ctx context.Context, req *schema.UnifiedRequest) (*provider.ProviderCall, error) {
	if req == nil {
		return nil, fmt.Errorf("gemini: %q nil request", a.spec.ProviderName)
	}
	wire, degraded := translate.ToGeminiUpstreamRequest(req)
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("gemini: %q marshal: %w", a.spec.ProviderName, err)
	}
	header := make(http.Header, len(a.spec.ExtraHeaders)+3)
	header.Set("Content-Type", "application/json")
	header.Set("Accept", "application/json")
	header.Set("x-goog-api-key", a.spec.APIKey)
	for k, v := range a.spec.ExtraHeaders {
		header.Set(k, v)
	}
	return &provider.ProviderCall{
		Method:   http.MethodPost,
		URL:      a.actionURL(req.Model, req.Stream),
		Header:   header,
		Body:     body,
		Stream:   req.Stream,
		Model:    req.Model,
		Original: req,
		Degraded: degraded,
	}, nil
}

// Parse implements provider.Provider for non-streaming responses: the
// Gemini wire body is normalized through translate.
func (a *Adapter) Parse(ctx context.Context, call *provider.ProviderCall, resp *http.Response) (*schema.Response, error) {
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("gemini: %q nil upstream response", a.spec.ProviderName)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, readUpstreamError(a.spec.ProviderName, resp.StatusCode, resp.Body)
	}
	var wire translate.GeminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, fmt.Errorf("gemini: %q decode: %w", a.spec.ProviderName, err)
	}
	parsed := translate.FromGeminiUpstreamResponse(&wire)
	parsed.ProviderName = a.spec.ProviderName
	if call != nil {
		if parsed.Model == "" {
			parsed.Model = call.Model
		}
		if parsed.ID == "" {
			parsed.ID = call.Model
		}
	}
	return parsed, nil
}

// readUpstreamError normalizes a non-2xx body into a typed UpstreamError.
func readUpstreamError(name string, status int, body io.Reader) error {
	b, err := io.ReadAll(io.LimitReader(body, maxErrorBody))
	if err != nil {
		return fmt.Errorf("gemini: %q read error body: %w", name, err)
	}
	return &provider.UpstreamError{
		Provider: name,
		Status:   status,
		Body:     bytes.TrimSpace(b),
	}
}
