// dedup.go 是 Session-Dedup 阶段（docs/05 4.2）：会话历史中的重复
// 轮次去重。LLM API 无状态、客户端每轮回传全量历史，重复轮次（重试
// 重发、反复粘贴的相同内容、agent 循环插入的重复上下文）直接体现
// 在单轮 messages 内——因此本阶段是纯函数式的轮内去重，无跨请求
// 状态；跨请求累计计数（去重率统计可见）仅用于观测，不影响行为。
package compression

import (
	"sync"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// DedupConfig 是 Session-Dedup 的可调参数。
type DedupConfig struct {
	// MinTokens 是触发阈值：粗估 token 低于此值不值得跑。
	MinTokens int
	// RecencyGuard 是尾部保护条数（与 gate 的 recency 规则对齐，
	// 默认 2）：最近几条即使与前文重复也原样保留。
	RecencyGuard int
}

// DefaultDedupConfig 返回默认参数。
func DefaultDedupConfig() DedupConfig {
	return DedupConfig{MinTokens: 64, RecencyGuard: defaultRecencyWindow}
}

// DedupTotals 是跨请求累计观测计数（stats 可见性的聚合面）。
type DedupTotals struct {
	// Runs 是 Apply 实际执行次数（ShouldRun 通过）。
	Runs int64
	// MessagesRemoved 是累计去重掉的消息条数。
	MessagesRemoved int64
	// TokensSaved 是累计节省的粗估 token（去重率的分子）。
	TokensSaved int64
	// TokensBefore 是累计压缩前粗估 token（去重率的分母）。
	TokensBefore int64
}

// Rate 返回累计去重率（节省 token / 压缩前 token）；无运行记录时为 0。
func (t DedupTotals) Rate() float64 {
	if t.TokensBefore == 0 {
		return 0
	}
	return float64(t.TokensSaved) / float64(t.TokensBefore)
}

// DedupStage 折叠会话历史中的重复轮次：role+全文 相同的 user/
// assistant 纯文本消息只保留首次出现；system 与 tool 链消息有结构
// 语义（gate 亦保护）不参与；尾部 RecencyGuard 条原样不动。
type DedupStage struct {
	config DedupConfig

	mu     sync.Mutex
	totals DedupTotals
}

// NewDedupStage 构造去重阶段；cfg 零值字段取默认。
func NewDedupStage(cfg DedupConfig) *DedupStage {
	if cfg.MinTokens <= 0 {
		cfg.MinTokens = 64
	}
	if cfg.RecencyGuard <= 0 {
		cfg.RecencyGuard = defaultRecencyWindow
	}
	return &DedupStage{config: cfg}
}

// Name 实现 CompressionStage。
func (s *DedupStage) Name() string { return "session_dedup" }

// ShouldRun 实现 CompressionStage：太短的上下文不值得去重。
func (s *DedupStage) ShouldRun(sc *StageContext) bool {
	return sc.EstimatedTokens >= s.config.MinTokens &&
		sc.MessageCount > s.config.RecencyGuard
}

// Apply 实现 CompressionStage：保留每份 role+全文 的首次出现。
func (s *DedupStage) Apply(msgs []schema.Message) ([]schema.Message, CompressionStats, error) {
	before := EstimateTokens(msgs)
	out := make([]schema.Message, 0, len(msgs))
	seen := make(map[string]struct{}, len(msgs))
	guardStart := len(msgs) - min(s.config.RecencyGuard, len(msgs))
	removed := 0
	for i, m := range msgs {
		if i >= guardStart || !deduplicable(m) {
			out = append(out, m)
			continue
		}
		key := m.Role + "\x00" + m.Content.TextOf()
		if _, dup := seen[key]; dup {
			removed++
			continue
		}
		seen[key] = struct{}{}
		out = append(out, m)
	}
	after := EstimateTokens(out)
	stats := CompressionStats{
		Stage:        s.Name(),
		Applied:      true,
		BeforeTokens: before,
		AfterTokens:  after,
		Saved:        before - after,
	}
	s.record(removed, before, stats.Saved)
	return out, stats, nil
}

// Totals 返回跨请求累计计数快照（观测用）。
func (s *DedupStage) Totals() DedupTotals {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totals
}

// record 累计观测计数。
func (s *DedupStage) record(removed, before, saved int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totals.Runs++
	s.totals.MessagesRemoved += int64(removed)
	s.totals.TokensBefore += int64(before)
	s.totals.TokensSaved += int64(saved)
}

// deduplicable 判定一条消息是否可安全参与去重：仅 user/assistant
// 的纯文本消息（无工具调用骨架、无工具结果身份）。
func deduplicable(m schema.Message) bool {
	switch m.Role {
	case schema.RoleUser, schema.RoleAssistant:
	default:
		return false
	}
	return len(m.ToolCalls) == 0 && m.ToolCallID == ""
}
