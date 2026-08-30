// gateway.go 是 MCP 工具集访问网关运行态的数据通道：复用
// Dashboard v0 的 JSON API（/dashboard/api/*，），Bearer 网关 key
// 鉴权。stdio 模式的 MCP server 是独立进程（无 router/QuotaTracker
// 内存态），挂网关进程的 Streamable HTTP 模式亦走同一视图——工具
// 实现单一、数据口径与浏览器一致；个人网关规模下环回一跳可忽略。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// ProviderInfo 是 providers 视图的一行（dashboard API 口径）。
type ProviderInfo struct {
	Name          string     `json:"name"`
	Models        int        `json:"models"`
	LatencyMS     float64    `json:"latency_ms"`
	SuccessRate   float64    `json:"success_rate"`
	LastSuccessAt *string    `json:"last_success_at"`
	Cooldowns     []Cooldown `json:"cooldowns"`
}

// Cooldown 是一条活跃隔离。
type Cooldown struct {
	Scope  string `json:"scope"`
	Model  string `json:"model,omitempty"`
	Until  string `json:"until"`
	Reason string `json:"reason"`
}

// ProvidersResult 是 providers 视图整体。
type ProvidersResult struct {
	Providers   []ProviderInfo `json:"providers"`
	ModelsTotal int            `json:"models_total"`
}

// KeyInfo 是 keys 视图的一行。
type KeyInfo struct {
	Provider  string `json:"provider"`
	Source    string `json:"source"`
	Label     string `json:"label,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// UsageLimits 是四窗口配额声明（0=未设限）。
type UsageLimits struct {
	RPM int   `json:"rpm"`
	RPD int   `json:"rpd"`
	TPM int64 `json:"tpm"`
	TPD int64 `json:"tpd"`
}

// UsageInfo 是 usage 视图的一行。
type UsageInfo struct {
	Provider string      `json:"provider"`
	RPM      int         `json:"rpm"`
	RPD      int         `json:"rpd"`
	TPM      int64       `json:"tpm"`
	TPD      int64       `json:"tpd"`
	Limits   UsageLimits `json:"limits"`
	Headroom float64     `json:"headroom"`
}

// UsageResult 是 usage 视图整体。
type UsageResult struct {
	Usage        []UsageInfo `json:"usage"`
	CacheEntries int64       `json:"cache_entries"`
}

// AuditEntry 是审计视图的一行（dashboard API 口径）。
type AuditEntry struct {
	ID        int64   `json:"id"`
	TS        string  `json:"ts"`
	Endpoint  string  `json:"endpoint"`
	Model     string  `json:"model"`
	Provider  string  `json:"provider"`
	Status    int     `json:"status"`
	TokensIn  int     `json:"tokens_in"`
	TokensOut int     `json:"tokens_out"`
	LatencyMS float64 `json:"latency_ms"`
	TTFTMS    float64 `json:"ttft_ms"`
	CacheHit  bool    `json:"cache_hit"`
	ErrKind   string  `json:"error_kind,omitempty"`
	Combo     string  `json:"combo,omitempty"`
}

// AuditResult 是审计视图整体。
type AuditResult struct {
	Requests []AuditEntry `json:"requests"`
}

// GatewayView 经 dashboard API 读取网关运行态。
type GatewayView struct {
	base  string // 如 http://127.0.0.1:20130（无尾斜杠）
	token string // 网关 key（Bearer）
	hc    *http.Client
}

// NewGatewayView 装配视图；hc 为 nil 时用默认客户端。
func NewGatewayView(base, token string, hc *http.Client) *GatewayView {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &GatewayView{base: base, token: token, hc: hc}
}

// get 取一个 dashboard JSON 端点并解码到 out。
func (g *GatewayView) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.base+path, nil)
	if err != nil {
		return fmt.Errorf("agent: build request %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	resp, err := g.hc.Do(req)
	if err != nil {
		return fmt.Errorf("agent: gateway unreachable at %s: %w", g.base, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("agent: gateway %s: HTTP %d: %s", path, resp.StatusCode, body)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("agent: decode %s: %w", path, err)
	}
	return nil
}

// Providers 返回 provider 健康视图。
func (g *GatewayView) Providers(ctx context.Context) (*ProvidersResult, error) {
	var out ProvidersResult
	if err := g.get(ctx, "/dashboard/api/providers", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Keys 返回 key 来源视图。
func (g *GatewayView) Keys(ctx context.Context) ([]KeyInfo, error) {
	var out struct {
		Keys []KeyInfo `json:"keys"`
	}
	if err := g.get(ctx, "/dashboard/api/keys", &out); err != nil {
		return nil, err
	}
	return out.Keys, nil
}

// Audit 返回最近请求审计行（；limit<=0 用默认 20）。
func (g *GatewayView) Audit(ctx context.Context, limit int, provider, endpoint string) (*AuditResult, error) {
	if limit <= 0 {
		limit = 20
	}
	path := fmt.Sprintf("/dashboard/api/audit?limit=%d", limit)
	if provider != "" {
		path += "&provider=" + url.QueryEscape(provider)
	}
	if endpoint != "" {
		path += "&endpoint=" + url.QueryEscape(endpoint)
	}
	var out AuditResult
	if err := g.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Usage 返回配额用量与缓存计数视图。
func (g *GatewayView) Usage(ctx context.Context) (*UsageResult, error) {
	var out UsageResult
	if err := g.get(ctx, "/dashboard/api/usage", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// WhoamiResult 是 whoami 端点视图（stdio 模式启动时确定工具集）。
type WhoamiResult struct {
	Kind   string   `json:"kind"`
	Scopes []string `json:"scopes"`
}

// Whoami 返回当前 token 的身份与 scope 集。
func (g *GatewayView) Whoami(ctx context.Context) (*WhoamiResult, error) {
	var out WhoamiResult
	if err := g.get(ctx, "/dashboard/api/whoami", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ModelInfo 是模型目录的一行。
type ModelInfo struct {
	Provider      string `json:"provider"`
	ID            string `json:"id"`
	ContextWindow int64  `json:"context_window"`
}

// Models 返回模型目录快照（provider × model × 上下文窗口）。
func (g *GatewayView) Models(ctx context.Context) ([]ModelInfo, error) {
	var out struct {
		Models []ModelInfo `json:"models"`
	}
	if err := g.get(ctx, "/dashboard/api/models", &out); err != nil {
		return nil, err
	}
	return out.Models, nil
}

// HealthResult 是聚合健康快照。
type HealthResult struct {
	Status          string `json:"status"`
	Version         string `json:"version"`
	Providers       int    `json:"providers"`
	ActiveCooldowns int    `json:"active_cooldowns"`
}

// Health 返回网关聚合健康视图。
func (g *GatewayView) Health(ctx context.Context) (*HealthResult, error) {
	var out HealthResult
	if err := g.get(ctx, "/dashboard/api/health", &out); err != nil {
		return nil, err
	}
	return &out, nil
}
