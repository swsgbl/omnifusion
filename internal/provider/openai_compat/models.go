// models.go 实现 openai_compat 的实时模型目录拉取（catalog sync
// 消费）：GET {base}/models 是 OpenAI 兼容标准面，各家字段口径在
// context 长度上略有出入（OpenRouter context_length / Groq
// context_window / LLM7 context_window.tokens），两个都收，取非零值。
package openai_compat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
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
			ID            string        `json:"id"`
			ContextLength lenientWindow `json:"context_length"`
			ContextWindow lenientWindow `json:"context_window"`
			IsFree        *bool         `json:"isFree"`
			Pricing       *listingPrice `json:"pricing"`
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
		// isFree 未标记（nil）的清单不区分免费池，只有显式 false 才排除。
		if a.spec.FreeOnly && m.IsFree != nil && !*m.IsFree {
			continue
		}
		ctxLen := int64(m.ContextLength)
		if ctxLen == 0 {
			ctxLen = int64(m.ContextWindow)
		}
		// 免费的两种上游证据：isFree:true，或 OpenRouter 式 prompt/
		// completion 双零定价（字符串数字）。都无则 Free=false——
		// 未声明不等于免费。
		free := m.IsFree != nil && *m.IsFree
		if !free && m.Pricing != nil && m.Pricing.zero() {
			free = true
		}
		out = append(out, provider.ModelInfo{ID: m.ID, ContextWindow: ctxLen, Free: free})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("openai_compat: %q models response carried no entries", a.spec.ProviderName)
	}
	return out, nil
}

// Keyless implements provider.Anonymous：免凭据实例化（注册表
// optional_key 且未配 key）。此时上游的 401/403 说的是模型档位
//（付费模型要求登录），不是 key 失效——路由层只锁模型。
func (a *Adapter) Keyless() bool { return a.spec.APIKey == "" }

// IsAggregator implements provider.Aggregator（注册表 aggregator: true）。
func (a *Adapter) IsAggregator() bool { return a.spec.Aggregator }

// listingPrice 是 OpenRouter 式定价块（USD / 1M token）。
type listingPrice struct {
	Prompt     lenientNumber `json:"prompt"`
	Completion lenientNumber `json:"completion"`
}

// zero 报告"输入输出都显式为 0"。缺字段/非数字解码成未声明（present
// =false），不会被当成免费——只有定价块明明白白写了两个 0 才算。
func (p listingPrice) zero() bool {
	return p.Prompt.present && p.Completion.present &&
		p.Prompt.value == 0 && p.Completion.value == 0
}

// lenientNumber 接受 JSON 数字或数字字符串（OpenRouter 的定价是
// 字符串）。present 区分"字段缺失/不是数字"与"显式 0"。
type lenientNumber struct {
	value   float64
	present bool
}

func (n *lenientNumber) UnmarshalJSON(b []byte) error {
	v, err := strconv.ParseFloat(strings.Trim(strings.TrimSpace(string(b)), `"`), 64)
	*n = lenientNumber{value: v, present: err == nil}
	return nil // 非数字一律按"未声明"处理：清单字段漂移不能打掉整份目录
}

// lenientWindow 接受三种形态：裸数字（Groq context_window: 131072）、
// 带 tokens 字段的对象（LLM7: {"tokens":512000,"chars":null}）、以及
// 无法识别的任意形状。无法识别一律解码成 0（窗口未知 → 路由不过滤），
// 绝不让一个厂商的字段漂移把整份清单打掉——2026-11-25 用户实撞：
// LLM7 把 context_window 从数字改成对象，解码报错后该家目录连续
// 多周停在旧快照。
type lenientWindow int64

func (w *lenientWindow) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return nil
	}
	if s[0] == '{' {
		var obj struct {
			Tokens lenientWindow `json:"tokens"`
		}
		if json.Unmarshal(b, &obj) != nil {
			return nil
		}
		*w = obj.Tokens
		return nil
	}
	var n lenientNumber
	_ = n.UnmarshalJSON(b)
	if !n.present {
		return nil
	}
	*w = lenientWindow(n.value)
	return nil
}
