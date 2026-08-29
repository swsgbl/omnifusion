// caveman.go 是 Caveman 规则压缩阶段（docs/05 4.4，docs/02：规则
// 文本压缩标准档 ~30%）。对 guard 窗口外的 user/assistant 长文本消息
// 应用确定性词面规则：冗长短语缩写、填充词删除、空白规整。只动词面
// 不动结构——system 全文（gate 保全）与 tool 链（结构保全）不碰，
// recency 尾部不动。
package compression

import (
	"regexp"
	"strings"
	"sync"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// cavemanPhrases 是冗长短语 → 紧凑形（按长度降序替换避免前缀截断）。
var cavemanPhrases = [][2]string{
	{"in spite of the fact that", "although"},
	{"due to the fact that", "because"},
	{"it is important to note that", "note"},
	{"at this point in time", "now"},
	{"in the event that", "if"},
	{"a large number of", "many"},
	{"for the purpose of", "for"},
	{"with regard to", "about"},
	{"in order to", "to"},
}

// cavemanFillers 是纯填充词（词边界删除，大小写不敏感）。
var cavemanFillers = []string{
	"please", "basically", "actually", "literally", "really",
	"very", "quite", "simply", "certainly", "just",
}

var (
	phraseRes    []*regexp.Regexp
	fillerRe     *regexp.Regexp
	multiSpaceRe = regexp.MustCompile(`[[:space:]]{2,}`)
)

func init() {
	for _, p := range cavemanPhrases {
		phraseRes = append(phraseRes,
			regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(p[0])+`\b`))
	}
	quoted := make([]string, len(cavemanFillers))
	for i, f := range cavemanFillers {
		quoted[i] = regexp.QuoteMeta(f)
	}
	// 尾随 [ ]? 一并吃掉，避免删除后残留双空格/行首空格。
	fillerRe = regexp.MustCompile(`(?i)\b(` + strings.Join(quoted, "|") + `)\b[ ]?`)
}

// cavemanText 应用全套规则（导出供样例报告与测试直接调用）。
func cavemanText(text string) string {
	out := text
	for i, p := range cavemanPhrases {
		out = phraseRes[i].ReplaceAllString(out, p[1])
	}
	out = fillerRe.ReplaceAllString(out, "")
	return normalizeWhitespace(out)
}

// normalizeWhitespace 规整空白：行内连续空白折一、行尾空白去除、
// 连续空行折一空行。行结构保留（可读性与 token 都受益）。
func normalizeWhitespace(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = multiSpaceRe.ReplaceAllString(line, " ")
		line = strings.TrimRight(line, " \t\r")
		if line == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		out = append(out, line)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

// CavemanConfig 是规则压缩的可调参数。
type CavemanConfig struct {
	// MinTokens 是整请求触发阈值（粗估 token）。
	MinTokens int
	// MinTextChars 是单条消息文本的触发阈值。
	MinTextChars int
	// RecencyGuard 是尾部保护条数（与 gate 对齐）。
	RecencyGuard int
}

// DefaultCavemanConfig 返回默认参数。
func DefaultCavemanConfig() CavemanConfig {
	return CavemanConfig{
		MinTokens:    64,
		MinTextChars: 200,
		RecencyGuard: defaultRecencyWindow,
	}
}

// CavemanTotals 是跨请求累计观测计数（节省率报告的聚合面）。
type CavemanTotals struct {
	// Runs 是 Apply 执行次数。
	Runs int64
	// MessagesRewritten 是被改写的消息条数。
	MessagesRewritten int64
	// CharsBefore / CharsSaved 是改写前后字符数与节省。
	CharsBefore int64
	CharsSaved  int64
}

// Rate 返回累计节省率（0..1）；无记录时为 0。
func (t CavemanTotals) Rate() float64 {
	if t.CharsBefore == 0 {
		return 0
	}
	return float64(t.CharsSaved) / float64(t.CharsBefore)
}

// CavemanStage 实现 CompressionStage：规则压缩长文本消息。
type CavemanStage struct {
	config CavemanConfig

	mu     sync.Mutex
	totals CavemanTotals
}

// NewCavemanStage 构造阶段；cfg 零值字段取默认。
func NewCavemanStage(cfg CavemanConfig) *CavemanStage {
	d := DefaultCavemanConfig()
	if cfg.MinTokens <= 0 {
		cfg.MinTokens = d.MinTokens
	}
	if cfg.MinTextChars <= 0 {
		cfg.MinTextChars = d.MinTextChars
	}
	if cfg.RecencyGuard <= 0 {
		cfg.RecencyGuard = d.RecencyGuard
	}
	return &CavemanStage{config: cfg}
}

// Name 实现 CompressionStage。
func (s *CavemanStage) Name() string { return "caveman" }

// ShouldRun 实现 CompressionStage。
func (s *CavemanStage) ShouldRun(sc *StageContext) bool {
	return sc.EstimatedTokens >= s.config.MinTokens
}

// Apply 实现 CompressionStage：改写 guard 外的长文本消息。
func (s *CavemanStage) Apply(msgs []schema.Message) ([]schema.Message, CompressionStats, error) {
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
		trimmed := cavemanText(text)
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
func (s *CavemanStage) Totals() CavemanTotals {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totals
}

func (s *CavemanStage) record(rewritten, before, saved int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totals.Runs++
	s.totals.MessagesRewritten += int64(rewritten)
	s.totals.CharsBefore += int64(before)
	s.totals.CharsSaved += int64(saved)
}

// cavemanEligible 判定可安全词面压缩的消息：user/assistant 纯文本
// 单 part，无工具骨架（与 dedup 的可动域一致）。
func cavemanEligible(m schema.Message) bool {
	return deduplicable(m) && singleTextOf(m) != ""
}
