// gate.go 是压缩管线的尾置保真门：每个阶段的产出
// 必须通过全部确定性规则，否则产出被丢弃、请求回退该阶段输入。
// 第一版规则只做结构保全（不比语义相似度）——文本可变短，骨架不可动。
package compression

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

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

// DefaultFidelityGate 返回默认规则集（确定性第一版 + Phase 5 结构
// 骨架保全）。
func DefaultFidelityGate() *FidelityGate {
	return NewFidelityGate(
		Rule{Name: "messages_non_empty", Check: messagesNonEmpty},
		Rule{Name: "system_preserved", Check: systemPreserved},
		Rule{Name: "tool_calls_preserved", Check: toolCallsPreserved},
		Rule{Name: "tool_results_preserved", Check: toolResultsPreserved},
		Rule{Name: "structured_integrity", Check: structuredIntegrity},
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

// structuredIntegrity 要求结构化内容骨架存活（蓝图 Phase 5 硬规则：
// 结构化内容优先结构感知压缩，盲文本压缩不得破坏结构骨架）。before
// 中每个「唯一」结构化值——整 part 有效 JSON（按语义规范化：键序/
// 空白/数值写法不敏感）与围栏代码块（按折叠规范化 + 语言标签）——在
// after 中必须至少存活一份。重复折叠（dedup 2→1）合法；截断、改写或
// 丢弃唯一结构化内容一律拒绝（fail-closed：宁可少压不可吐坏 JSON/
// 残码给模型）。
func structuredIntegrity(before, after []schema.Message) error {
	jsonBefore, codeBefore := collectStructured(before)
	if len(jsonBefore) == 0 && len(codeBefore) == 0 {
		return nil
	}
	jsonAfter, codeAfter := collectStructured(after)
	for k := range jsonBefore {
		if jsonAfter[k] == 0 {
			return errString("structured JSON value lost: " + truncate(k))
		}
	}
	for k := range codeBefore {
		if codeAfter[k] == 0 {
			return errString("code block lost: " + truncate(k))
		}
	}
	return nil
}

// collectStructured 收集一轮消息中的结构化值指纹（规范化文本 → 计数）。
func collectStructured(msgs []schema.Message) (jsonSet, codeSet map[string]int) {
	jsonSet, codeSet = map[string]int{}, map[string]int{}
	for _, m := range msgs {
		for _, p := range m.Content.Parts {
			text := strings.TrimSpace(p.Text)
			if text == "" {
				continue
			}
			if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
				if c, ok := canonicalJSON(text); ok {
					jsonSet[c]++
					continue
				}
			}
			for _, block := range fencedFingerprints(text) {
				codeSet[block]++
			}
		}
	}
	return jsonSet, codeSet
}

// canonicalJSON 返回 JSON 的语义规范形：解码（UseNumber 保精度）再
// 编码——map 键序排序、空白消除、数值写法归一；非法 JSON ok=false。
func canonicalJSON(text string) (string, bool) {
	if !json.Valid([]byte(text)) {
		return "", false
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// fencedFingerprints 提取文本中全部围栏代码块的指纹：语言标签 +
// 折叠体（与 FoldingStage 同一 FoldCodeLines——折叠阶段自身天然通过，
// 白空格外的任何改动都会改变指纹）。
func fencedFingerprints(text string) []string {
	var out []string
	for _, m := range fenceRe.FindAllStringSubmatch(text, -1) {
		out = append(out, strings.TrimSpace(m[1])+"\n"+FoldCodeLines(m[2]))
	}
	return out
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
