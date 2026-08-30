// Package compression 是 L4 压缩层（，）：管线化的
// 请求压缩，尾置 Fidelity Gate 保证保真——任何阶段产出不达标即丢弃
// 该阶段结果回退原文，压缩失败永不影响请求成功（ 规则 3）。
package compression

import (
	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// CompressionStage 是压缩管线的一个阶段（ 冻结接口）。
// 实现必须纯函数化：不改传入切片，产出新切片。
type CompressionStage interface {
	// Name 是阶段标识（stats/配置引用）。
	Name() string
	// ShouldRun 自适应触发：上下文不够长/不适用的请求跳过本阶段。
	ShouldRun(sc *StageContext) bool
	// Apply 压缩一轮消息，返回新消息与统计（ 冻结签名，
	// 阶段自需的请求侧信息经 ShouldRun 的 StageContext 判定后自持）。
	// 错误=本阶段失败，管线回退到本阶段输入继续（原文直传语义）。
	Apply(msgs []schema.Message) ([]schema.Message, CompressionStats, error)
}

// StageContext 是阶段共享的请求侧上下文（触发依据与跨层输入）。
type StageContext struct {
	// Model 是请求目标模型（模型感知的阈值预留位）。
	Model string
	// SessionID 是 sticky 会话标识（Session-Dedup 用，）。
	SessionID string
	// EstimatedTokens 是进入管线前的粗估 token（触发阈值用）。
	EstimatedTokens int
	// MessageCount 是消息条数（轻量触发条件用）。
	MessageCount int
}

// NewStageContext 依据消息预计算触发字段。
func NewStageContext(model, sessionID string, msgs []schema.Message) *StageContext {
	return &StageContext{
		Model:           model,
		SessionID:       sessionID,
		EstimatedTokens: EstimateTokens(msgs),
		MessageCount:    len(msgs),
	}
}

// CompressionStats 是单阶段的一次执行记录（观测与 4.5 跨层输入）。
type CompressionStats struct {
	Stage string
	// Skipped 为 true 表示 ShouldRun 判定不适用，本阶段未执行。
	Skipped bool
	// Applied 为 true 表示本阶段产出被采纳。
	Applied bool
	// GateRejected 非 nil 表示 Fidelity Gate 拦截，产出被丢弃。
	GateRejected error
	// Err 非 nil 表示阶段自身失败（原文直传语义）。
	Err error
	// BeforeTokens / AfterTokens 是本阶段进出的粗估 token。
	BeforeTokens int
	AfterTokens  int
	// Saved 是本阶段净节省（负值=变长，gate 之外的无效压缩标记）。
	Saved int
}

// EstimateTokens 粗估一轮消息的 token 用量：文本按 4 字符/token
// （OpenAI 经验值），每条消息计固定开销，tool_calls 按 name+
// arguments 计权。绝对值不求准——跨层（4.5）只消费相对比较。
func EstimateTokens(msgs []schema.Message) int {
	total := 0
	for _, m := range msgs {
		total += 4 // 每消息角色/分隔开销
		total += len(m.Content.TextOf()) / 4
		for _, c := range m.ToolCalls {
			total += (len(c.Function.Name) + len(c.Function.Arguments)) / 4
		}
	}
	return total
}
