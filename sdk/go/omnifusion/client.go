// Package omnifusion 是 OmniFusion 网关的 Go SDK（ 预留
// `sdk/go/omnifusion`）：OpenAI 兼容面（/v1/chat/completions）的进程外
// 客户端——非流式 Chat、SSE 流式 ChatStream、类型化 StatusError。
// 纯标准库、零 internal 依赖，可被任意 Go 项目独立引用。
package omnifusion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client 是网关客户端。零值不可用，经 NewClient 构造；并发安全
// （http.Client 与只读字段）。
type Client struct {
	base string // 形如 http://127.0.0.1:9090（不带尾斜杠）
	key  string // 网关 key（ofg-，Authorization: Bearer）
	hc   *http.Client
}

// NewClient 构造客户端。baseURL 形如 "http://127.0.0.1:9090"；
// gatewayKey 经 `ofd gateway-key` 打印。
func NewClient(baseURL, gatewayKey string) *Client {
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		key:  gatewayKey,
		hc:   &http.Client{Timeout: 5 * time.Minute},
	}
}

// StatusError 是网关/上游的非 2xx 响应（鉴权失败 401、模型缺失 404、
// 上游不可用 502 等），Body 截断到 2KiB 便于日志。
type StatusError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *StatusError) Error() string {
	msg := e.Body
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return fmt.Sprintf("omnifusion: %s: %s", e.Status, msg)
}

// Chat 发起一次非流式补全。req.Stream 恒被置 false（流式走 ChatStream）。
// 语义缓存命中时响应携带 CacheHit()=true。
func (c *Client) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	q := req.clone()
	q.Stream = false
	body, err := json.Marshal(q)
	if err != nil {
		return nil, fmt.Errorf("omnifusion: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("omnifusion: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("omnifusion: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &StatusError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       truncate(string(raw), 2048),
		}
	}
	var out ChatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("omnifusion: decode response: %w", err)
	}
	out.cacheHit = resp.Header.Get("X-Omnifusion-Cache") == "hit"
	return &out, nil
}

// Models 拉取网关目录快照（/v1/models，跨 provider 平铺）。
func (c *Client) Models(ctx context.Context) ([]ModelInfo, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("omnifusion: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("omnifusion: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &StatusError{
			StatusCode: resp.StatusCode, Status: resp.Status,
			Body: truncate(string(raw), 2048),
		}
	}
	var out struct {
		Data []ModelInfo `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("omnifusion: decode models: %w", err)
	}
	return out.Data, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
