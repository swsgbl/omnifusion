// Package intelligence 承载 L5 智能层：语义缓存 v1 为
// 「精确匹配」层——缓存键 = 影响生成结果的全部请求字段的确定性序列化
// （encoding/json 对结构体按声明序、对 map 按键排序编码，序列化确定）
// → SHA-256。近似层（sqlite-vec embedding 相似命中） 预留：表列
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
// Generation 是目录代际（Cache 2.0）：目录 sync 实际变更后递增，
// 旧代际条目自然失效——@quality 等别名重新决议到不同模型时，旧响应
// 不再被当作新答案返回（CacheKeySpec 的 resolved-model 维度）。
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
	Generation     uint64             `json:"generation,omitempty"`
}

// CacheKey 计算请求的精确缓存键（hex SHA-256）。代际 0（未接入目录
// 代际的调用方）与旧键形状兼容。
func CacheKey(req *schema.UnifiedRequest) string {
	return cacheKeyWithGeneration(req, 0)
}

// cacheKeyWithGeneration 在键载荷中纳入目录代际。
func cacheKeyWithGeneration(req *schema.UnifiedRequest, generation uint64) string {
	k := cacheKeyPayload{
		Model: req.Model, Messages: req.Messages,
		Temperature: req.Temperature, TopP: req.TopP,
		MaxTokens: req.MaxTokens, Stop: req.Stop,
		Tools: req.Tools, ToolChoice: req.ToolChoice,
		ResponseFormat: req.ResponseFormat, Seed: req.Seed, User: req.User,
		Generation: generation,
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

// SemCache 是精确层语义缓存（ L5：查询→命中直接返回；
// 未命中→响应成功后异步回写）。v1 直查 SQLite：本地 WAL 点查
// <1ms 级，满足 4.6 重复请求 TTFT<10ms 验收。
type SemCache struct {
	st         *store.Store
	ttl        time.Duration
	maxEntries int
	now        func() time.Time
	writes     atomic.Int64
	// genFn 返回目录代际（server 装配为 catalog.Generation；nil=恒 0）。
	gen func() uint64
}

// NewSemCache 构造缓存：ttl 为条目有效期；maxEntries 为容量上限，
// 每 64 次回写触发一次淘汰（保留最新）。
func NewSemCache(st *store.Store, ttl time.Duration, maxEntries int) *SemCache {
	return &SemCache{st: st, ttl: ttl, maxEntries: maxEntries, now: time.Now}
}

// SetGenerationFunc 装配目录代际查询（Cache 2.0 键维度：目录 sync 实际
// 变更后代际递增，同请求的缓存键随之改变——旧条目自然过期，无需清理）。
func (c *SemCache) SetGenerationFunc(f func() uint64) { c.gen = f }

// keyFor 计算含代际的缓存键。
func (c *SemCache) keyFor(req *schema.UnifiedRequest) string {
	var gen uint64
	if c.gen != nil {
		gen = c.gen()
	}
	return cacheKeyWithGeneration(req, gen)
}

// Lookup 查缓存：命中且未过期返回响应。未装配（nil）、流式请求、
// 策略判定 BYPASS（工具调用/非确定性——Cache 2.0 策略门）、上下文已
// 取消、任何存储/解码失败一律视为未命中——缓存永不阻塞主路径、永不
// 把坏数据当命中。
func (c *SemCache) Lookup(ctx context.Context, req *schema.UnifiedRequest) (*schema.Response, bool) {
	if c == nil || c.st == nil || req.Stream {
		return nil, false
	}
	if v := EvaluateCachePolicy(req, nil); !v.Allowed {
		return nil, false
	}
	if ctx.Err() != nil {
		return nil, false
	}
	e, err := c.st.GetSemanticCache(c.keyFor(req))
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
// 请求取消中断）：序列化响应并 upsert。策略门同时判请求与响应——
// 含 tool_calls 的响应不可回写（Cache 2.0）。任何失败静默放弃——缓存
// 写失败不得影响已成功返回的响应。
func (c *SemCache) WriteBack(ctx context.Context, req *schema.UnifiedRequest, resp *schema.Response) {
	if c == nil || c.st == nil || req.Stream || resp == nil {
		return
	}
	if v := EvaluateCachePolicy(req, resp); !v.Allowed {
		return
	}
	if ctx.Err() != nil {
		return
	}
	payload, err := json.Marshal(resp)
	if err != nil {
		return
	}
	if err := c.st.PutSemanticCache(c.keyFor(req), payload, c.now().Unix()); err != nil {
		return
	}
	if c.writes.Add(1)%64 == 0 {
		_, _ = c.st.TrimSemanticCache(c.maxEntries)
	}
}
