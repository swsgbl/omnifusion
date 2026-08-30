// mlrouter.go 是 ML Router v1（：默认纯 Go 启发式，学
// RouteLLM 的弱/强二分 + 置信度阈值；ONNX 分类器是可选构建对比项，
// 不进默认二进制）。请求难度经 DifficultyClassifier 打分
// （0..1，1=需强模型）：≥ 阈值走强档，否则弱档；主档在前、另一档
// 殿后作 failover（隔离/配额/打分等弹性机制在路由主循环照常生效）。
// 本包不依赖 routing（L5→L3 跨层经显式接口注入，与 同模式）。
package intelligence

import (
	"sync"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// DefaultMLThreshold 是弱/强分档缺省阈值：难度落灰色地带时走弱档，
// 由另一档 failover 兜底（学 RouteLLM 置信区间的保守端）。
const DefaultMLThreshold = 0.55

// TierWeak / TierStrong 是两档路由的档位名（观测/日志面）。
const (
	TierWeak   = "weak"
	TierStrong = "strong"
)

// MLTarget 是一档路由目标（provider + 发往该上游的模型）。
type MLTarget struct {
	Provider string
	Model    string
}

// RouteDecision 是一次 ML 路由的决策。Members 是尝试序列：主档在前、
// 另一档殿后作 failover。
type RouteDecision struct {
	Tier       string  // "weak" | "strong"
	Difficulty float64 // 分类器难度分 0..1
	Confidence float64 // 1 - Difficulty（档位置信度）
	Members    []MLTarget
}

// MLRouter 执行弱/强二分路由。零值不可用：经 NewMLRouter
// 构造。Classifier 可替换（启发式与 ONNX 实现互换同接口）。
type MLRouter struct {
	Weak       MLTarget
	Strong     MLTarget
	Threshold  float64 // <=0 或 >=1 → DefaultMLThreshold
	Classifier DifficultyClassifier

	mu      sync.Mutex
	weakN   int
	strongN int
}

// NewMLRouter 构造弱/强二分路由器（默认启发式分类器 + 默认阈值）。
func NewMLRouter(weak, strong MLTarget) *MLRouter {
	return &MLRouter{Weak: weak, Strong: strong, Classifier: HeuristicClassifier{}}
}

// Route 对请求做难度分类并产出尝试序列（主档在前）。逐请求分类是
// 本意：不缓存难度、不粘会话——同一会话的下一问可能恰好是难题。
func (m *MLRouter) Route(req *schema.UnifiedRequest) RouteDecision {
	d := m.classifier().Difficulty(req)
	tier, first, second := TierWeak, m.Weak, m.Strong
	if d >= m.EffectiveThreshold() {
		tier, first, second = TierStrong, m.Strong, m.Weak
	}
	m.record(tier)
	return RouteDecision{
		Tier:       tier,
		Difficulty: d,
		Confidence: 1 - d,
		Members:    []MLTarget{first, second},
	}
}

// MLTotals 是 ML 路由的累计观测（验收面：A/B 报告对比规则路由
// 的分流与成本差异）。
type MLTotals struct {
	Decisions int
	Weak      int
	Strong    int
}

// Totals 返回累计决策计数（并发安全快照）。
func (m *MLRouter) Totals() MLTotals {
	m.mu.Lock()
	defer m.mu.Unlock()
	return MLTotals{Decisions: m.weakN + m.strongN, Weak: m.weakN, Strong: m.strongN}
}

// EffectiveThreshold 返回生效阈值（0 或越界值 → DefaultMLThreshold）。
func (m *MLRouter) EffectiveThreshold() float64 {
	if m.Threshold > 0 && m.Threshold < 1 {
		return m.Threshold
	}
	return DefaultMLThreshold
}

func (m *MLRouter) classifier() DifficultyClassifier {
	if m.Classifier == nil {
		return HeuristicClassifier{}
	}
	return m.Classifier
}

func (m *MLRouter) record(tier string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tier == TierStrong {
		m.strongN++
		return
	}
	m.weakN++
}
