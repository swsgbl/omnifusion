// semantic.go 是语义压缩规则档（ 6.2，）：对长文档文本做
// 多语言分句 + 词频信息量打分 + 按保留率裁低信息句，保序重组。
// 只裁句不造句——被裁句整体消失、保留句逐字不变，保真由结构保证；
// 尾部 RecencyGuard 条与 system/tool 链不碰（gate 亦保全）。纯 Go
// 零模型依赖；LLMLingua-2 级神经压缩走可选 sidecar 档
// （semantic_sidecar.go），默认二进制不阻塞（学习型模型不进默认二进制）。
package compression

import (
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// 语义档默认参数：600 粗估 token 以下不值得跑（caveman 已覆盖轻量
// 场景）；单文本 600 字符起步——短文本裁句得不偿失。
const (
	defaultSemanticMinTokens    = 600
	defaultSemanticMinTextChars = 600
	defaultSemanticKeepRate     = 0.5
)

// SemanticConfig 是规则档可调参数。
type SemanticConfig struct {
	// MinTokens 是整请求触发阈值（粗估 token）。
	MinTokens int
	// MinTextChars 是单条消息文本的触发阈值。
	MinTextChars int
	// RecencyGuard 是尾部保护条数（与 gate 对齐）。
	RecencyGuard int
	// Rate 是句保留率（0.1–0.9，0 = 取装配期包级配置或默认）。
	Rate float64
}

// DefaultSemanticConfig 返回默认参数。
func DefaultSemanticConfig() SemanticConfig {
	return SemanticConfig{
		MinTokens:    defaultSemanticMinTokens,
		MinTextChars: defaultSemanticMinTextChars,
		RecencyGuard: defaultRecencyWindow,
		Rate:         defaultSemanticKeepRate,
	}
}

// SemanticTotals 是跨请求累计观测计数（节省率报告的聚合面）。
type SemanticTotals struct {
	// Runs 是 Apply 执行次数。
	Runs int64
	// MessagesRewritten 是被改写的消息条数。
	MessagesRewritten int64
	// CharsBefore / CharsSaved 是改写前后字符数与节省。
	CharsBefore int64
	CharsSaved  int64
}

// Rate 返回累计节省率（0..1）；无记录时为 0。
func (t SemanticTotals) Rate() float64 {
	if t.CharsBefore == 0 {
		return 0
	}
	return float64(t.CharsSaved) / float64(t.CharsBefore)
}

// SemanticStage 实现 CompressionStage：规则语义压缩长文档文本。
type SemanticStage struct {
	config SemanticConfig

	mu     sync.Mutex
	totals SemanticTotals
}

// NewSemanticStage 构造阶段；cfg 零值字段取默认。
func NewSemanticStage(cfg SemanticConfig) *SemanticStage {
	d := DefaultSemanticConfig()
	if cfg.MinTokens <= 0 {
		cfg.MinTokens = d.MinTokens
	}
	if cfg.MinTextChars <= 0 {
		cfg.MinTextChars = d.MinTextChars
	}
	if cfg.RecencyGuard <= 0 {
		cfg.RecencyGuard = d.RecencyGuard
	}
	return &SemanticStage{config: cfg}
}

// Name 实现 CompressionStage。
func (s *SemanticStage) Name() string { return "semantic" }

// ShouldRun 实现 CompressionStage。
func (s *SemanticStage) ShouldRun(sc *StageContext) bool {
	return sc.EstimatedTokens >= s.config.MinTokens
}

// Apply 实现 CompressionStage：改写 guard 外的长文本消息。
func (s *SemanticStage) Apply(msgs []schema.Message) ([]schema.Message, CompressionStats, error) {
	rate := s.config.Rate
	if rate <= 0 {
		rate = configuredSemanticRate()
	}
	before := EstimateTokens(msgs)
	out := make([]schema.Message, len(msgs))
	copy(out, msgs)
	guardStart := len(msgs) - min(s.config.RecencyGuard, len(msgs))
	rewritten, charsBefore, charsSaved := 0, 0, 0
	for i, m := range msgs {
		if i >= guardStart || !cavemanEligible(m) {
			continue
		}
		text := singleTextOf(m)
		if len(text) < s.config.MinTextChars {
			continue
		}
		trimmed := semanticCompress(text, rate)
		if trimmed == text {
			continue
		}
		rewritten++
		charsBefore += len(text)
		charsSaved += len(text) - len(trimmed)
		out[i].Content = schema.NewTextContent(trimmed)
	}
	after := EstimateTokens(out)
	stats := CompressionStats{
		Stage:        s.Name(),
		Applied:      true,
		BeforeTokens: before,
		AfterTokens:  after,
		Saved:        before - after,
	}
	s.record(rewritten, charsBefore, charsSaved)
	return out, stats, nil
}

// Totals 返回跨请求累计计数快照。
func (s *SemanticStage) Totals() SemanticTotals {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totals
}

func (s *SemanticStage) record(rewritten, before, saved int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totals.Runs++
	s.totals.MessagesRewritten += int64(rewritten)
	s.totals.CharsBefore += int64(before)
	s.totals.CharsSaved += int64(saved)
}

// semanticCompress 是规则档核心：分句 → 打分 → 裁低信息句 → 保序
// 重组。<4 句不动（裁无可裁）；首末句与代码块受保护；近重复句
// （token 集 Jaccard>0.8）只留首现。k=ceil(n×rate) 是保留句数目标。
func semanticCompress(text string, rate float64) string {
	sents := splitSentences(text)
	n := len(sents)
	if n < 4 {
		return text
	}
	k := int(math.Ceil(float64(n) * clampRate(rate)))
	if k >= n {
		return text
	}
	dup := nearDupFlags(sents)
	scores := semanticScores(sents)
	keep, cand := keepPlan(sents, dup)
	kept := 0
	for _, ok := range keep {
		if ok {
			kept++
		}
	}
	sort.Slice(cand, func(a, b int) bool { return scores[cand[a]] > scores[cand[b]] })
	for _, i := range cand {
		if kept >= k {
			break
		}
		keep[i] = true
		kept++
	}
	var b strings.Builder
	for i, s := range sents {
		if keep[i] {
			b.WriteString(s.text)
		}
	}
	return b.String()
}

// keepPlan 产出初始保留位（仅受保护句）与候选序（可按分补选的句：
// 非保护、非近重复）。
func keepPlan(sents []sentence, dup []bool) (keep []bool, cand []int) {
	n := len(sents)
	keep = make([]bool, n)
	for i, s := range sents {
		if i == 0 || i == n-1 || s.code { // 首末句与代码块恒留
			keep[i] = true
			continue
		}
		if dup[i] {
			continue // 近重复后出句恒裁
		}
		cand = append(cand, i)
	}
	return keep, cand
}

// nearDupFlags 标记后出近重复句：与任一先现句 token 集 Jaccard>0.8。
func nearDupFlags(sents []sentence) []bool {
	sets := make([]map[string]bool, len(sents))
	for i, s := range sents {
		sets[i] = map[string]bool{}
		for _, t := range tokenize(s.text) {
			sets[i][t] = true
		}
	}
	dup := make([]bool, len(sents))
	for i := range sets {
		for j := 0; j < i; j++ {
			if jaccard(sets[i], sets[j]) > 0.8 {
				dup[i] = true
				break
			}
		}
	}
	return dup
}
