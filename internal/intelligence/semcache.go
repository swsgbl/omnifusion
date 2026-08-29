// Package intelligence 承载 L5 智能层（docs/04 §3）：语义缓存 v1 为
// 「精确匹配」层——缓存键 = 影响生成结果的全部请求字段的确定性序列化
// （encoding/json 对结构体按声明序、对 map 按键排序编码，序列化确定）
// → SHA-256。近似层（sqlite-vec embedding 相似命中）M6 预留：表列
// embedding_blob v1 恒 NULL。
package intelligence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/store"
)

// cacheKeyPayload 是参与缓存键的字段集。Stream 不参与：缓存与传输
// 形态无关，同一请求流式/非流式共享同一键（v1 仅在非流式路径查写）。
// Extra 不参与：未建模透传字段语义不可判定，宁 miss 勿错 hit。
type cacheKeyPayload struct {
	Model          string             `json:"model"`
	Messages       []schema.Message   `json:"messages"`
	Temperature    *float64           `json:"temperature,omitempty"`
	TopP           *float64           `json:"top_p,omitempty"`
	MaxTokens      *int               `json:"max_tokens,omitempty"`
	Stop           []string           `json:"stop,omitempty"`
	Tools          []schema.Tool      `json:"tools,omitempty"`
	ToolChoice     *schema.ToolChoice `json:"tool_choice,omitempty"`
	ResponseFormat json.RawMessage    `json:"response_format,omitempty"`
	Seed           *int64             `json:"seed,omitempty"`
	User           string             `json:"user,omitempty"`
}

// CacheKey 计算请求的精确缓存键（hex SHA-256）。
func CacheKey(req *schema.UnifiedRequest) string {
	k := cacheKeyPayload{
		Model: req.Model, Messages: req.Messages,
		Temperature: req.Temperature, TopP: req.TopP,
		MaxTokens: req.MaxTokens, Stop: req.Stop,
		Tools: req.Tools, ToolChoice: req.ToolChoice,
		ResponseFormat: req.ResponseFormat, Seed: req.Seed, User: req.User,
	}
	b, err := json.Marshal(k)
	if err != nil {
		// 参与字段均含合法 JSON（源自 Unmarshal 校验后的 IR）；
		// 若仍失败则退化为空载荷键：仍确定，只是命中率归零。
		b = nil
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SemCache 是精确层语义缓存（docs/04 §3 L5：查询→命中直接返回；
// 未命中→响应成功后异步回写）。v1 直查 SQLite：本地 WAL 点查
// <1ms 级，满足 docs/05 4.6 重复请求 TTFT<10ms 验收。
type SemCache struct {
	st         *store.Store
	ttl        time.Duration
	maxEntries int
	now        func() time.Time
	writes     atomic.Int64
}

// NewSemCache 构造缓存：ttl 为条目有效期；maxEntries 为容量上限，
// 每 64 次回写触发一次淘汰（保留最新）。
func NewSemCache(st *store.Store, ttl time.Duration, maxEntries int) *SemCache {
	return &SemCache{st: st, ttl: ttl, maxEntries: maxEntries, now: time.Now}
}

// Lookup 查缓存：命中且未过期返回响应。未装配（nil）、流式请求、
// 上下文已取消、任何存储/解码失败一律视为未命中——缓存永不阻塞
// 主路径、永不把坏数据当命中。
func (c *SemCache) Lookup(ctx context.Context, req *schema.UnifiedRequest) (*schema.Response, bool) {
	if c == nil || c.st == nil || req.Stream {
		return nil, false
	}
	if ctx.Err() != nil {
		return nil, false
	}
	e, err := c.st.GetSemanticCache(CacheKey(req))
	if err != nil {
		return nil, false
	}
	age := c.now().Unix() - e.Timestamp
	if age < 0 {
		age = 0 // 时钟回拨容忍
	}
	if time.Duration(age)*time.Second >= c.ttl {
		return nil, false // 过期：不返回、不删除（下次回写覆盖）
	}
	var resp schema.Response
	if err := json.Unmarshal(e.Payload, &resp); err != nil {
		return nil, false
	}
	return &resp, true
}

// WriteBack 响应成功后的回写路径（调用方以 context.WithoutCancel 防
// 请求取消中断）：序列化响应并 upsert。任何失败静默放弃——缓存写
// 失败不得影响已成功返回的响应。
func (c *SemCache) WriteBack(ctx context.Context, req *schema.UnifiedRequest, resp *schema.Response) {
	if c == nil || c.st == nil || req.Stream || resp == nil {
		return
	}
	if ctx.Err() != nil {
		return
	}
	payload, err := json.Marshal(resp)
	if err != nil {
		return
	}
	if err := c.st.PutSemanticCache(CacheKey(req), payload, c.now().Unix()); err != nil {
		return
	}
	if c.writes.Add(1)%64 == 0 {
		_, _ = c.st.TrimSemanticCache(c.maxEntries)
	}
}
