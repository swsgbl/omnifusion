// models.go 实现 openai_compat 的实时模型目录拉取（catalog sync
// 消费）：GET {base}/models 是 OpenAI 兼容标准面，各家字段口径在
// context 长度上略有出入（OpenRouter context_length / Groq
// context_window），两个都收，取非零值。
package openai_compat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/swsgbl/omnifusion/internal/provider"
)

// maxModelsBody bounds the /models response (largest catalogs run to a
// few MB; runaway responses get truncated here).
const maxModelsBody = 8 << 20 // 8 MiB

// ListModels implements provider.Provider: GET {base}/models with the
// same auth shape as chat completions; non-2xx becomes the typed
// UpstreamError the router already classifies.
func (a *Adapter) ListModels(ctx context.Context) ([]provider.ModelInfo, error) {
	url := strings.TrimRight(a.spec.BaseURL, "/") + "/models"
	if a.spec.AuthStyle == AuthQuery && a.spec.APIKey != "" {
		url += "?" + a.spec.APIQueryParam + "=" + a.spec.APIKey
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("openai_compat: %q build models request: %w", a.spec.ProviderName, err)
	}
	header := make(http.Header, len(a.spec.ExtraHeaders)+2)
	header.Set("Accept", "application/json")
	switch a.spec.AuthStyle {
	case AuthHeader:
		if a.spec.APIKey != "" {
			header.Set(a.spec.APIKeyHeader, a.spec.APIKey)
		}
	case "", AuthBearer:
		if a.spec.APIKey != "" {
			header.Set("Authorization", "Bearer "+a.spec.APIKey)
		}
	}
	for k, v := range a.spec.ExtraHeaders {
		header.Set(k, v)
	}
	req.Header = header

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai_compat: %q models transport: %w", a.spec.ProviderName, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, &provider.UpstreamError{
			Provider: a.spec.ProviderName,
			Status:   resp.StatusCode,
			Body:     body,
		}
	}

	var payload struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int64  `json:"context_length"`
			ContextWindow int64  `json:"context_window"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxModelsBody)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("openai_compat: %q decode models: %w", a.spec.ProviderName, err)
	}

	out := make([]provider.ModelInfo, 0, len(payload.Data))
	for _, m := range payload.Data {
		if m.ID == "" {
			continue
		}
		ctxLen := m.ContextLength
		if ctxLen == 0 {
			ctxLen = m.ContextWindow
		}
		out = append(out, provider.ModelInfo{ID: m.ID, ContextWindow: ctxLen})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("openai_compat: %q models response carried no entries", a.spec.ProviderName)
	}
	return out, nil
}
