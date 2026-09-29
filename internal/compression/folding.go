// folding.go 是结构感知折叠阶段（蓝图 Phase 5）：对结构化内容
// （JSON / 围栏代码块，含 SQL 与配置——它们以围栏或纯 JSON 到达）
// 只做无损折叠——JSON 走 json.Compact 纯空白消除，代码块做行级空白
// 规整（行尾空白/连续空行/块内首尾空行），任何 part 解析失败或折叠
// 后不更短都原样保留。蓝图硬规则：结构化内容优先结构感知压缩，
// 盲文本压缩阶段不得破坏结构骨架——该不变量由 gate 的
// structured_integrity 规则强制。
package compression

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// fenceRe 匹配一个围栏代码块：```lang\nbody```（lang 可空、允许尾随
// 信息；body 非贪婪到闭合围栏）。
var fenceRe = regexp.MustCompile("(?s)```([^\n]*)\n(.*?)```")

// FoldingConfig 是结构感知折叠的可调参数。
type FoldingConfig struct {
	// MinTokens 是整请求触发阈值（粗估 token）：更小的请求不值得跑。
	MinTokens int
	// MinTextChars 是单 part 触发阈值：更短的文本折叠无收益。
	MinTextChars int
	// RecencyGuard 是尾部保护条数（与 gate recency 规则对齐，默认 2）。
	RecencyGuard int
}

// DefaultFoldingConfig 返回默认参数。
func DefaultFoldingConfig() FoldingConfig {
	return FoldingConfig{MinTokens: 64, MinTextChars: 120, RecencyGuard: defaultRecencyWindow}
}

// NewFoldingStage 构造结构感知折叠阶段（零值配置按最小阈值语义运行）。
func NewFoldingStage(cfg FoldingConfig) *FoldingStage { return &FoldingStage{cfg: cfg} }

// FoldingStage 实现 CompressionStage：JSON/代码块无损折叠。
type FoldingStage struct {
	cfg FoldingConfig
}

// Name 是阶段标识（combo 配置引用 "folding"）。
func (s *FoldingStage) Name() string { return "folding" }

// ShouldRun 按整请求粗估 token 触发；part 级判定在 Apply 内做（便宜）。
func (s *FoldingStage) ShouldRun(sc *StageContext) bool {
	return sc != nil && sc.EstimatedTokens >= s.cfg.MinTokens
}

// Apply 逐消息（尾部 RecencyGuard 条除外）逐 part 折叠结构化内容；
// 输入切片与未命中 part 永不就地修改。
func (s *FoldingStage) Apply(msgs []schema.Message) ([]schema.Message, CompressionStats, error) {
	guard := max(s.cfg.RecencyGuard, 1)
	cut := len(msgs) - guard
	if cut < 0 {
		cut = 0
	}
	out := make([]schema.Message, len(msgs))
	copy(out, msgs)
	for i := 0; i < cut; i++ {
		m := out[i]
		if len(m.Content.Parts) == 0 {
			continue
		}
		parts := make([]schema.Part, len(m.Content.Parts))
		copy(parts, m.Content.Parts)
		changed := false
		for j := range parts {
			if parts[j].Type != schema.PartText || len(parts[j].Text) < s.cfg.MinTextChars {
				continue
			}
			if folded := FoldStructured(parts[j].Text); folded != parts[j].Text {
				parts[j].Text = folded
				changed = true
			}
		}
		if changed {
			m.Content = schema.Content{Parts: parts}
			out[i] = m
		}
	}
	return out, CompressionStats{Stage: s.Name()}, nil
}

// FoldStructured 折叠单个文本：整 part 有效 JSON → json.Compact（只删
// token 间空白，字节内容无损）；含围栏代码块 → 行级空白规整。其余
// 情形（无结构/解析失败/折叠后不更短）原样返回——折叠永不使文本变长。
func FoldStructured(text string) string {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		if !json.Valid([]byte(trimmed)) {
			return text
		}
		var buf bytes.Buffer
		if err := json.Compact(&buf, []byte(trimmed)); err != nil {
			return text
		}
		if buf.Len() < len(text) {
			return buf.String()
		}
		return text
	}
	if strings.Contains(text, "```") {
		folded := fenceRe.ReplaceAllStringFunc(text, foldFence)
		if len(folded) < len(text) {
			return folded
		}
	}
	return text
}

// foldFence 折叠单个围栏块；体未变化时整块原样返回。重建时保持
// 闭合围栏前有换行的约定（FoldCodeLines 产出永不以换行结尾）。
func foldFence(block string) string {
	m := fenceRe.FindStringSubmatch(block)
	if m == nil {
		return block
	}
	folded := FoldCodeLines(m[2])
	if folded == m[2] {
		return block
	}
	return "```" + m[1] + "\n" + folded + "\n```"
}

// FoldCodeLines 规整代码行：去行尾空白、连续空行折叠为单空行、去块内
// 首尾空行——任何非空白字符（含缩进）零损伤。
func FoldCodeLines(body string) string {
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	blankRun := false
	for _, ln := range lines {
		trimmedRight := strings.TrimRight(ln, " \t")
		if trimmedRight == "" {
			blankRun = true
			continue
		}
		if blankRun && len(out) > 0 {
			out = append(out, "")
		}
		blankRun = false
		out = append(out, trimmedRight)
	}
	return strings.Join(out, "\n")
}
