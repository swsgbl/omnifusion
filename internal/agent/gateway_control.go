// gateway_control.go 是 GatewayView 的控制面通道（M5.2）：路由钉选/
// 隔离清除/默认压缩组合的写操作与组合清单、钉选状态查询，走网关
// scope 化控制 API（Bearer token 决定权限——越权时网关 403，错误
// 以工具错误面回传客户端）。
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// post 向网关控制端点发 JSON 请求并解码响应。
func (g *GatewayView) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("agent: encode %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.base+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("agent: build request %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.hc.Do(req)
	if err != nil {
		return fmt.Errorf("agent: gateway unreachable at %s: %w", g.base, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("agent: gateway %s: HTTP %d: %s", path, resp.StatusCode, rb)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("agent: decode %s: %w", path, err)
	}
	return nil
}

// PinResult 是钉选设置/清除的响应。
type PinResult struct {
	Pinned string  `json:"pinned"`
	Until  *string `json:"until"`
}

// RouteStatusResult 是路由控制面状态（钉选 + 各 provider 活跃隔离数）。
type RouteStatusResult struct {
	Pinned          string         `json:"pinned"`
	Until           *string        `json:"until"`
	ActiveCooldowns map[string]int `json:"active_cooldowns"`
}

// RouteStatus 返回钉选与活跃隔离视图。
func (g *GatewayView) RouteStatus(ctx context.Context) (*RouteStatusResult, error) {
	var out RouteStatusResult
	if err := g.get(ctx, "/dashboard/api/route/status", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RoutePin 设置全局钉选（provider 空 = 清除；ttlSeconds<=0 网关侧
// 默认 30 分钟）。
func (g *GatewayView) RoutePin(ctx context.Context, provider string, ttlSeconds int) (*PinResult, error) {
	var out PinResult
	in := struct {
		Provider   string `json:"provider"`
		TTLSeconds int    `json:"ttl_seconds,omitempty"`
	}{Provider: provider, TTLSeconds: ttlSeconds}
	if err := g.post(ctx, "/dashboard/api/route/pin", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RouteUnpin 清除全局钉选。
func (g *GatewayView) RouteUnpin(ctx context.Context) (*PinResult, error) {
	var out PinResult
	if err := g.post(ctx, "/dashboard/api/route/unpin", struct{}{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClearResult 是隔离清除的响应。
type ClearResult struct {
	Provider string `json:"provider"`
	Cleared  int    `json:"cleared"`
}

// ClearCooldowns 人工清除一个 provider 的全部隔离（内存 + 持久）。
func (g *GatewayView) ClearCooldowns(ctx context.Context, provider string) (*ClearResult, error) {
	var out ClearResult
	in := struct {
		Provider string `json:"provider"`
	}{Provider: provider}
	if err := g.post(ctx, "/dashboard/api/route/cooldowns/clear", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ComboInfo 是组合清单一行（Stages 空 = 纯路由组合）。
type ComboInfo struct {
	Name     string   `json:"name"`
	Stages   []string `json:"stages,omitempty"`
	Compress bool     `json:"compress"`
}

// CombosResult 是组合清单整体。
type CombosResult struct {
	Combos       []ComboInfo `json:"combos"`
	DefaultCombo string      `json:"default_combo"`
}

// Combos 返回组合清单与当前默认组合。
func (g *GatewayView) Combos(ctx context.Context) (*CombosResult, error) {
	var out CombosResult
	if err := g.get(ctx, "/dashboard/api/combos", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DefaultComboResult 是默认组合设置/清除的响应。
type DefaultComboResult struct {
	DefaultCombo string `json:"default_combo"`
}

// SetDefaultCombo 设置默认压缩组合（空 = 清除；未知组合网关侧 400）。
func (g *GatewayView) SetDefaultCombo(ctx context.Context, combo string) (*DefaultComboResult, error) {
	var out DefaultComboResult
	in := struct {
		Combo string `json:"combo"`
	}{Combo: combo}
	if err := g.post(ctx, "/dashboard/api/compression/default", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
