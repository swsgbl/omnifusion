// gate.go 是压缩管线的尾置保真门（docs/04 §4.3）：每个阶段的产出
// 必须通过全部确定性规则，否则产出被丢弃、请求回退该阶段输入。
// 第一版规则只做结构保全（不比语义相似度）——文本可变短，骨架不可动。
package compression

import (
	"bytes"
	"errors"
	"sort"
	"strconv"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// defaultRecencyWindow 是 recency 规则默认保护的消息条数。
const defaultRecencyWindow = 2

// FidelityRule 是一条保真规则：before 是阶段输入，after 是阶段产出。
// 返回 nil 表示通过；非 nil 的 error 会被包成 GateRejection。
type FidelityRule func(before, after []schema.Message) error

// Rule 是命名规则（可独立测试、可替换组装）。
type Rule struct {
	Name  string
	Check FidelityRule
}

// FidelityGate 聚合一组规则，按注册顺序逐条检查，首错即返回。
type FidelityGate struct {
	rules []Rule
}

// NewFidelityGate 组装规则集；无参时为空门（全通过，仅测试用）。
func NewFidelityGate(rules ...Rule) *FidelityGate {
	return &FidelityGate{rules: rules}
}

// DefaultFidelityGate 返回默认规则集（M4.1 确定性第一版）。
func DefaultFidelityGate() *FidelityGate {
	return NewFidelityGate(
		Rule{Name: "messages_non_empty", Check: messagesNonEmpty},
		Rule{Name: "system_preserved", Check: systemPreserved},
		Rule{Name: "tool_calls_preserved", Check: toolCallsPreserved},
		Rule{Name: "tool_results_preserved", Check: toolResultsPreserved},
		Rule{Name: "recency_preserved", Check: RecencyRule(defaultRecencyWindow)},
	)
}

// Check 校验一轮阶段产出；nil 表示通过。
func (g *FidelityGate) Check(before, after []schema.Message) *GateRejection {
	for _, r := range g.rules {
		if err := r.Check(before, after); err != nil {
			return &GateRejection{Rule: r.Name, Err: err}
		}
	}
	return nil
}

// GateRejection 是一次拦截：Rule 命名触发规则，Err 是具体原因。
type GateRejection struct {
	Rule string
	Err  error
}

func (e *GateRejection) Error() string {
	return "fidelity gate: " + e.Rule + ": " + e.Err.Error()
}

func (e *GateRejection) Unwrap() error { return e.Err }

// messagesNonEmpty 要求产出非空（输入非空时）且不含空壳消息。
func messagesNonEmpty(before, after []schema.Message) error {
	if len(before) > 0 && len(after) == 0 {
		return errString("compression emptied the message list")
	}
	for i, m := range after {
		if len(m.Content.Parts) == 0 && len(m.ToolCalls) == 0 && m.ToolCallID == "" {
			return errString("produced empty message at index " + itoa(i))
		}
	}
	return nil
}

// systemPreserved 要求 before 中每条 system 消息的全文在 after 中
// 仍以 system 角色出现（多重集包含，允许次数不减）。
func systemPreserved(before, after []schema.Message) error {
	want := countSystem(before)
	got := countSystem(after)
	for _, text := range sortedKeys(want) {
		if got[text] < want[text] {
			return errString("system message dropped or altered: " + truncate(text))
		}
	}
	return nil
}

// toolCallsPreserved 要求 assistant 每个 tool_call 的 id 与函数名不变。
func toolCallsPreserved(before, after []schema.Message) error {
	have := map[string]string{}
	for _, m := range after {
		for _, c := range m.ToolCalls {
			have[c.ID] = c.Function.Name
		}
	}
	for _, m := range before {
		for _, c := range m.ToolCalls {
			name, ok := have[c.ID]
			if !ok {
				return errString("tool_call " + c.ID + " dropped")
			}
			if name != c.Function.Name {
				return errString("tool_call " + c.ID + " renamed to " + name)
			}
		}
	}
	return nil
}

// toolResultsPreserved 要求 role=tool 消息条数不减且 ToolCallID 全部
// 保留——文本内容可压缩变短，工具结果的结构骨架不可丢（给 4.3
// ToolOutputFilter 留出合法空间）。
func toolResultsPreserved(before, after []schema.Message) error {
	wantIDs := map[string]bool{}
	for _, m := range before {
		if m.Role == schema.RoleTool {
			wantIDs[m.ToolCallID] = true
		}
	}
	if len(wantIDs) == 0 {
		return nil
	}
	gotIDs := map[string]bool{}
	for _, m := range after {
		if m.Role == schema.RoleTool {
			gotIDs[m.ToolCallID] = true
		}
	}
	for id := range wantIDs {
		if !gotIDs[id] {
			return errString("tool result for call " + id + " dropped")
		}
	}
	return nil
}

// RecencyRule 构造保护最后 window 条消息原样不动的规则（window<=0
// 视为 1）。
func RecencyRule(window int) FidelityRule {
	if window < 1 {
		window = 1
	}
	return func(before, after []schema.Message) error {
		n := min(window, len(before))
		if n == 0 {
			return nil
		}
		if len(after) < n {
			return errString("output shorter than recency window")
		}
		tail := before[len(before)-n:]
		outTail := after[len(after)-n:]
		for i := range tail {
			if !equalMessages(tail[i], outTail[i]) {
				return errString("recent message at offset " + itoa(i) + " modified")
			}
		}
		return nil
	}
}

// equalMessages 逐字段比较两条消息（含多模态部分的载荷与 Raw）。
func equalMessages(a, b schema.Message) bool {
	if a.Role != b.Role || a.Name != b.Name ||
		a.ToolCallID != b.ToolCallID || a.Refusal != b.Refusal {
		return false
	}
	if len(a.ToolCalls) != len(b.ToolCalls) {
		return false
	}
	for i := range a.ToolCalls {
		x, y := a.ToolCalls[i], b.ToolCalls[i]
		if x.ID != y.ID || x.Type != y.Type ||
			x.Function.Name != y.Function.Name ||
			x.Function.Arguments != y.Function.Arguments {
			return false
		}
	}
	if len(a.Content.Parts) != len(b.Content.Parts) {
		return false
	}
	for i := range a.Content.Parts {
		if !equalParts(a.Content.Parts[i], b.Content.Parts[i]) {
			return false
		}
	}
	return true
}

func equalParts(a, b schema.Part) bool {
	if a.Type != b.Type || a.Text != b.Text ||
		!bytes.Equal(a.Raw, b.Raw) {
		return false
	}
	if (a.ImageURL == nil) != (b.ImageURL == nil) ||
		(a.InputAudio == nil) != (b.InputAudio == nil) ||
		(a.File == nil) != (b.File == nil) {
		return false
	}
	if a.ImageURL != nil && *a.ImageURL != *b.ImageURL {
		return false
	}
	if a.InputAudio != nil && *a.InputAudio != *b.InputAudio {
		return false
	}
	if a.File != nil && *a.File != *b.File {
		return false
	}
	return true
}

func countSystem(msgs []schema.Message) map[string]int {
	counts := map[string]int{}
	for _, m := range msgs {
		if m.Role == schema.RoleSystem {
			counts[m.Content.TextOf()]++
		}
	}
	return counts
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// errString / itoa / truncate 是规则的错误构造小工具（保持规则体一行一判）。
func errString(s string) error { return errors.New(s) }
func itoa(i int) string        { return strconv.Itoa(i) }
func truncate(s string) string {
	r := []rune(s)
	if len(r) > 40 {
		return string(r[:40]) + "..."
	}
	return s
}
