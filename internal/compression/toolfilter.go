// toolfilter.go 是 ToolOutputFilter 阶段（ 4.3，RTK 思想：
// 命令感知的工具输出折叠）。Agent 会话里 role=tool 的消息常是终端
// 命令输出——大目录列表、构建日志、循环监控——结构同质、信息密度
// 头尾集中。本阶段从对应 tool_call 的 arguments 提取命令名，按命令
// 类型选择折叠策略：列表类保头、日志类保头尾、未知命令通用折叠
// （连续重复行折叠 + 超长截断）。gate 的 tool_results_preserved 只
// 要求结构与 ToolCallID 保全、文本可变短——本阶段正是该空间的第一
// 个合法用户。
package compression

import (
	"encoding/json"
	"path"
	"strings"
	"sync"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// ToolFilterConfig 是工具输出过滤的可调参数。
type ToolFilterConfig struct {
	// MinTokens 是整请求触发阈值（粗估 token）。
	MinTokens int
	// MinOutputChars 是单条工具输出的触发阈值（更短的输出不值得动）。
	MinOutputChars int
	// MaxLines 之内不截断；超出按 Head/Tail 保留。
	MaxLines int
	// HeadLines / TailLines 是截断时保留的头尾行数。
	HeadLines int
	// TailLines 同上。
	TailLines int
	// RecencyGuard 是尾部保护条数（与 gate 对齐）。
	RecencyGuard int
}

// DefaultToolFilterConfig 返回默认参数。
func DefaultToolFilterConfig() ToolFilterConfig {
	return ToolFilterConfig{
		MinTokens:      64,
		MinOutputChars: 500,
		MaxLines:       120,
		HeadLines:      40,
		TailLines:      20,
		RecencyGuard:   defaultRecencyWindow,
	}
}

// ToolFilterTotals 是跨请求累计观测计数。
type ToolFilterTotals struct {
	// OutputsFiltered 是被折叠的工具输出条数。
	OutputsFiltered int64
	// CharsBefore / CharsSaved 是折叠前后字符数与节省（压缩率的分子分母）。
	CharsBefore int64
	CharsSaved  int64
}

// Rate 返回累计压缩率（0..1）；无记录时为 0。
func (t ToolFilterTotals) Rate() float64 {
	if t.CharsBefore == 0 {
		return 0
	}
	return float64(t.CharsSaved) / float64(t.CharsBefore)
}

// ToolFilterStage 实现 CompressionStage：折叠长工具输出。
type ToolFilterStage struct {
	config ToolFilterConfig

	mu     sync.Mutex
	totals ToolFilterTotals
}

// NewToolFilterStage 构造阶段；cfg 零值字段取默认。
func NewToolFilterStage(cfg ToolFilterConfig) *ToolFilterStage {
	d := DefaultToolFilterConfig()
	if cfg.MinTokens <= 0 {
		cfg.MinTokens = d.MinTokens
	}
	if cfg.MinOutputChars <= 0 {
		cfg.MinOutputChars = d.MinOutputChars
	}
	if cfg.MaxLines <= 0 {
		cfg.MaxLines = d.MaxLines
	}
	if cfg.HeadLines <= 0 {
		cfg.HeadLines = d.HeadLines
	}
	if cfg.TailLines <= 0 {
		cfg.TailLines = d.TailLines
	}
	if cfg.RecencyGuard <= 0 {
		cfg.RecencyGuard = d.RecencyGuard
	}
	return &ToolFilterStage{config: cfg}
}

// Name 实现 CompressionStage。
func (s *ToolFilterStage) Name() string { return "tool_output_filter" }

// ShouldRun 实现 CompressionStage。
func (s *ToolFilterStage) ShouldRun(sc *StageContext) bool {
	return sc.EstimatedTokens >= s.config.MinTokens
}

// Apply 实现 CompressionStage：按命令类型折叠长工具输出文本。
func (s *ToolFilterStage) Apply(msgs []schema.Message) ([]schema.Message, CompressionStats, error) {
	before := EstimateTokens(msgs)
	commands := toolCommandsOf(msgs)
	out := make([]schema.Message, len(msgs))
	copy(out, msgs)
	guardStart := len(msgs) - min(s.config.RecencyGuard, len(msgs))
	filtered, charsBefore, charsSaved := 0, 0, 0
	for i, m := range msgs {
		if i >= guardStart || m.Role != schema.RoleTool {
			continue
		}
		text := singleTextOf(m)
		if len(text) < s.config.MinOutputChars {
			continue
		}
		folded := s.fold(text, commands[m.ToolCallID])
		if folded == text {
			continue
		}
		filtered++
		charsBefore += len(text)
		charsSaved += len(text) - len(folded)
		out[i].Content = schema.NewTextContent(folded)
	}
	after := EstimateTokens(out)
	stats := CompressionStats{
		Stage:        s.Name(),
		Applied:      true,
		BeforeTokens: before,
		AfterTokens:  after,
		Saved:        before - after,
	}
	s.record(filtered, charsBefore, charsSaved)
	return out, stats, nil
}

// Totals 返回跨请求累计计数快照。
func (s *ToolFilterStage) Totals() ToolFilterTotals {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totals
}

func (s *ToolFilterStage) record(filtered, before, saved int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totals.OutputsFiltered += int64(filtered)
	s.totals.CharsBefore += int64(before)
	s.totals.CharsSaved += int64(saved)
}

// fold 对一段工具输出文本应用命令感知的折叠策略。
func (s *ToolFilterStage) fold(text, command string) string {
	if isListCommand(command) { // 列表类：同质罗列，保头即可。
		return collapseRepeatedLines(truncateLines(text, s.limit(), 0))
	}
	// 日志类与未知命令：头尾信息密集 + 重复折叠。
	return collapseRepeatedLines(truncateLines(text, s.config.HeadLines, s.config.TailLines))
}

// limit 是列表类截断保留行数。
func (s *ToolFilterStage) limit() int { return s.config.HeadLines }

// singleTextOf 取纯文本单 part 消息的文本；其他形态（多 part/非文本）
// 不属于本阶段作用域。
func singleTextOf(m schema.Message) string {
	if len(m.Content.Parts) != 1 || m.Content.Parts[0].Type != schema.PartText {
		return ""
	}
	return m.Content.Parts[0].Text
}

// toolCommandsOf 建 ToolCallID → 命令名 映射（取 arguments 的
// command 字段首词 basename；无 command 字段的调用映射为空）。
func toolCommandsOf(msgs []schema.Message) map[string]string {
	commands := map[string]string{}
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			commands[c.ID] = commandNameOf(c.Function.Arguments)
		}
	}
	return commands
}

// commandNameOf 从工具调用 arguments JSON 提取命令名。
func commandNameOf(arguments string) string {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil || args.Command == "" {
		return ""
	}
	word := strings.Fields(args.Command)
	if len(word) == 0 {
		return ""
	}
	return path.Base(word[0])
}

// isListCommand 判定列表类命令（输出同质罗列，保头即可）。
func isListCommand(command string) bool {
	switch command {
	case "ls", "find", "du", "df", "ps", "grep", "rg", "tree":
		return true
	}
	return false
}

// collapseRepeatedLines 折叠连续重复行：保留一行并标注重复次数。
func collapseRepeatedLines(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		j := i
		for j+1 < len(lines) && strings.TrimSpace(lines[j+1]) == strings.TrimSpace(lines[i]) {
			j++
		}
		out = append(out, lines[i])
		if n := j - i + 1; n > 1 {
			out = append(out, "["+itoa(n)+" identical lines folded]")
		}
		i = j + 1
	}
	return strings.Join(out, "\n")
}

// truncateLines 超长输出保头尾：行数不多时原样返回。
func truncateLines(text string, head, tail int) string {
	lines := strings.Split(text, "\n")
	if head < 0 {
		head = 0
	}
	if len(lines) <= head+tail {
		return text
	}
	elided := len(lines) - head - tail
	var out []string
	out = append(out, lines[:head]...)
	out = append(out, "["+itoa(elided)+" lines elided]")
	if tail > 0 {
		out = append(out, lines[len(lines)-tail:]...)
	}
	return strings.Join(out, "\n")
}
