// Package provider defines the frozen Provider interface and its
// supporting types, per docs/04-架构设计.md §4.1. Adapters
// (declarative openai_compat or hand-written native) live in
// subpackages and are instantiated by the registry.
package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// ErrNotSupported is returned by optional interface methods (such as
// ListModels) when a provider does not implement them. Callers should
// then fall back to the registry's static model list.
var ErrNotSupported = errors.New("provider does not support this operation")

// Capability declares what a provider can do. The router's semantic
// matching (M2) and capability routing (M5) consume this; M1 only
// records it.
type Capability struct {
	// InputModalities accepted by the provider, e.g. "text", "image",
	// "audio", "video", "file".
	InputModalities []string
	// OutputModalities the provider can emit. M1 is text-only; audio
	// output arrives with the Realtime layer (M8).
	OutputModalities []string
	// Features are capability flags such as "tools", "json_schema",
	// "reasoning".
	Features []string
}

// Capabilities is the name used by the frozen interface sketch in
// docs/04-架构设计.md §4.1; Capability is the implementing struct.
type Capabilities = Capability

// HasFeature reports whether the capability set contains the named
// feature flag.
func (c Capability) HasFeature(name string) bool {
	for _, f := range c.Features {
		if f == name {
			return true
		}
	}
	return false
}

// SupportsInput reports whether the provider accepts the named input
// modality.
func (c Capability) SupportsInput(modality string) bool {
	for _, m := range c.InputModalities {
		if m == modality {
			return true
		}
	}
	return false
}

// ProviderCall is the outgoing HTTP call produced by Translate. Parse
// consumes it alongside the upstream response so adapters stay pure
// functions of (request, response).
type ProviderCall struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte

	// Stream is true when the upstream call was requested in SSE form.
	Stream bool
	// Model is the provider-side model id actually sent (after alias
	// rewriting), recorded for observability.
	Model string
	// Original carries the unified request so Parse can merge back
	// fields that providers omit from responses (tools, tool_choice,
	// response_format, reasoning_effort, per docs/03 ADR-002).
	Original *schema.UnifiedRequest
	// Degraded lists request fields this upstream cannot honor and the
	// translator dropped (M3.6, e.g. response_format on Anthropic). The
	// server merges it into the X-OmniFusion-Degraded response header —
	// never drop silently (docs/04 §7).
	Degraded []string
}

// Provider is the adapter contract frozen in docs/03 §4.1. Every
// upstream — declarative or native — implements exactly this surface.
type Provider interface {
	// Name returns the registry provider id.
	Name() string
	// Capabilities returns the declared capability set.
	Capabilities() Capability
	// Translate converts a unified request into the provider's wire
	// format.
	Translate(ctx context.Context, req *schema.UnifiedRequest) (*ProviderCall, error)
	// Parse converts the upstream HTTP response back into the unified
	// response shape.
	Parse(ctx context.Context, call *ProviderCall, resp *http.Response) (*schema.Response, error)
	// ListModels queries the provider's model catalog. Adapters that
	// cannot do this return ErrNotSupported and the registry serves
	// its static list instead.
	ListModels(ctx context.Context) ([]ModelInfo, error)
	// HTTPClient returns the provider-dedicated client. One client per
	// provider avoids the cross-provider P99 coupling documented in
	// the Bifrost postmortem (docs/01 item 1).
	HTTPClient() *http.Client
}

// ModelInfo describes one catalog entry. Fields grow as the router
// needs them; M1 keeps the identity and sizing basics.
type ModelInfo struct {
	ID            string `json:"id"`
	ContextWindow int64  `json:"context_window,omitempty"`
}

// UpstreamError is a non-2xx upstream result. The router (M1.6+) uses
// Status and Provider to classify failures for fallback; Body keeps
// the provider's original payload for logging and debugging.
type UpstreamError struct {
	Provider string
	Status   int
	Body     []byte
}

// Error implements error.
func (e *UpstreamError) Error() string {
	return fmt.Sprintf("provider %q upstream status %d: %s", e.Provider, e.Status, truncate(e.Body, 512))
}

// IsUpstream reports whether err is an UpstreamError and, if so,
// returns it.
func IsUpstream(err error) (*UpstreamError, bool) {
	var ue *UpstreamError
	if errors.As(err, &ue) {
		return ue, true
	}
	return nil, false
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
