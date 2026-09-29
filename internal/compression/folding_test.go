// folding_test.go 锁死结构感知折叠与 structured_integrity 门规则的
// 契约：折叠只做无损变换（JSON 语义等价、代码仅空白规整）、永不变长；
// 门要求唯一结构化值存活——截断/改写/丢弃被拒，压缩/折叠/去重放行。
package compression

import (
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

const prettyJSON = `{
  "model": "gpt",
  "tools": [
    {"name": "get_weather", "args": {"city": "SF"}}
  ],
  "count": 3
}`

func foldingFixture() []schema.Message {
	// 5 条消息：折叠目标在头部，尾部 2 条（recency 保护带）。
	return []schema.Message{
		textMsg(schema.RoleSystem, "You are helpful."),
		textMsg(schema.RoleUser, prettyJSON),
		textMsg(schema.RoleUser, "```python\ndef f():\n    return 1\n\n\n\n    \nx = 2\n\n```"),
		textMsg(schema.RoleUser, strings.Repeat("tail guard one ", 4)),
		textMsg(schema.RoleUser, "tail guard two"),
	}
}

func TestFoldStructuredJSONCompact(t *testing.T) {
	got := FoldStructured("  " + prettyJSON + "  ")
	if got == prettyJSON {
		t.Fatalf("pretty JSON was not compacted")
	}
	if c, ok := canonicalJSON(got); !ok || c != mustCanonical(t, prettyJSON) {
		t.Fatalf("compacted JSON is not semantically equal to original")
	}
	if strings.ContainsAny(got, "\n") {
		t.Fatalf("compacted JSON still contains newlines")
	}
}

func TestFoldStructuredJSONNeverGrows(t *testing.T) {
	compact := `{"a":1}`
	if got := FoldStructured(compact); got != compact {
		t.Fatalf("already-compact JSON must pass through unchanged, got %q", got)
	}
}

func TestFoldStructuredInvalidJSONUntouched(t *testing.T) {
	bad := "{\n  \"a\": 1,\n  \"b\": TRAILING\n}"
	if got := FoldStructured(bad); got != bad {
		t.Fatalf("invalid JSON must be untouched, got %q", got)
	}
}

func TestFoldStructuredCodeBlankFolding(t *testing.T) {
	src := "```sql\nSELECT 1;\n\n\n\n   \nSELECT 2;\n\n```"
	got := FoldStructured(src)
	want := "```sql\nSELECT 1;\n\nSELECT 2;\n```"
	if got != want {
		t.Fatalf("code folding mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestFoldStructuredCodeIndentPreserved(t *testing.T) {
	src := "```python\ndef f():\n        return 1\n```"
	if got := FoldStructured(src); got != src {
		t.Fatalf("leading indentation must be preserved, got %q", got)
	}
}

func TestFoldStructuredProseUntouched(t *testing.T) {
	prose := strings.Repeat("just prose with no structure at all ", 10)
	if got := FoldStructured(prose); got != prose {
		t.Fatalf("prose must pass through unchanged")
	}
}

func TestFoldingStageApply(t *testing.T) {
	st := NewFoldingStage(FoldingConfig{MinTokens: 1, MinTextChars: 1, RecencyGuard: 2})
	msgs := foldingFixture()
	sc := NewStageContext("m", "", msgs)
	if !st.ShouldRun(sc) {
		t.Fatalf("ShouldRun must fire above threshold")
	}
	out, _, err := st.Apply(msgs)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(out) != len(msgs) {
		t.Fatalf("message count changed: %d -> %d", len(msgs), len(out))
	}
	if out[1].Content.TextOf() == msgs[1].Content.TextOf() {
		t.Fatalf("JSON message was not folded")
	}
	if !strings.Contains(out[2].Content.TextOf(), "```sql") && !strings.Contains(out[2].Content.TextOf(), "```python") {
		t.Fatalf("code message mangled")
	}
	// 尾部 recency 保护带不动。
	if out[3].Content.TextOf() != msgs[3].Content.TextOf() || out[4].Content.TextOf() != msgs[4].Content.TextOf() {
		t.Fatalf("recency guard band was modified")
	}
	// 输入切片不被就地修改。
	if msgs[1].Content.TextOf() != prettyJSON {
		t.Fatalf("input slice was mutated in place")
	}
}

func TestFoldingStageShortPartsSkipped(t *testing.T) {
	st := NewFoldingStage(FoldingConfig{MinTokens: 1, MinTextChars: 500, RecencyGuard: 1})
	msgs := []schema.Message{textMsg(schema.RoleUser, prettyJSON)}
	out, _, err := st.Apply(msgs)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out[0].Content.TextOf() != prettyJSON {
		t.Fatalf("part below MinTextChars must be untouched")
	}
}

// droppingStage 模拟一个丢弃/改写结构化内容的阶段（证伪面）。
type droppingStage struct{ rewrite bool }

func (droppingStage) Name() string                 { return "dropping" }
func (droppingStage) ShouldRun(*StageContext) bool { return true }
func (d droppingStage) Apply(msgs []schema.Message) ([]schema.Message, CompressionStats, error) {
	out := make([]schema.Message, 0, len(msgs))
	for _, m := range msgs {
		t := strings.TrimSpace(m.Content.TextOf())
		if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
			if d.rewrite {
				out = append(out, textMsg(m.Role, t[:max(2, len(t)/2)])) // 暴力截断
			}
			continue // 默认整条丢弃
		}
		out = append(out, m)
	}
	return out, CompressionStats{Stage: d.Name()}, nil
}

// recencyGuards 是两条尾部保护消息（默认 recency 窗口=2：被测内容
// 必须在窗口之外，规则的失败才归属于被测规则本身）。
func recencyGuards() []schema.Message {
	return []schema.Message{
		textMsg(schema.RoleUser, "guard one"),
		textMsg(schema.RoleUser, "guard two"),
	}
}

func TestGateStructuredIntegrityJSONDroppedRejected(t *testing.T) {
	gate := DefaultFidelityGate()
	before := append([]schema.Message{textMsg(schema.RoleUser, prettyJSON),
		textMsg(schema.RoleUser, "summary please")}, recencyGuards()...)
	after, _, _ := droppingStage{}.Apply(before)
	rej := gate.Check(before, after)
	if rej == nil || rej.Rule != "structured_integrity" {
		t.Fatalf("dropping unique JSON must be rejected by structured_integrity, got %v", rej)
	}
}

func TestGateStructuredIntegrityJSONTruncatedRejected(t *testing.T) {
	gate := DefaultFidelityGate()
	before := append([]schema.Message{textMsg(schema.RoleUser, prettyJSON),
		textMsg(schema.RoleUser, "summary please")}, recencyGuards()...)
	after, _, _ := droppingStage{rewrite: true}.Apply(before)
	rej := gate.Check(before, after)
	if rej == nil || rej.Rule != "structured_integrity" {
		t.Fatalf("truncating unique JSON must be rejected by structured_integrity, got %v", rej)
	}
}

func TestGateStructuredIntegrityCompactedPasses(t *testing.T) {
	gate := DefaultFidelityGate()
	folded := FoldStructured(prettyJSON)
	if folded == prettyJSON {
		t.Fatalf("fixture must actually fold")
	}
	before := append([]schema.Message{textMsg(schema.RoleUser, prettyJSON)}, recencyGuards()...)
	after := append([]schema.Message{textMsg(schema.RoleUser, folded)}, recencyGuards()...)
	if rej := gate.Check(before, after); rej != nil {
		t.Fatalf("compacted JSON must pass, got %v", rej)
	}
}

func TestGateStructuredIntegrityDupCollapsePasses(t *testing.T) {
	gate := DefaultFidelityGate()
	dup := textMsg(schema.RoleUser, prettyJSON)
	before := append([]schema.Message{dup, dup, textMsg(schema.RoleUser, "go")}, recencyGuards()...)
	after := append([]schema.Message{dup, textMsg(schema.RoleUser, "go")}, recencyGuards()...)
	if rej := gate.Check(before, after); rej != nil {
		t.Fatalf("exact-duplicate collapse (2->1) must pass, got %v", rej)
	}
}

func TestGateStructuredIntegrityCodeLostRejected(t *testing.T) {
	gate := DefaultFidelityGate()
	code := textMsg(schema.RoleUser, "```python\ndef f():\n    return 1\n```")
	before := append([]schema.Message{textMsg(schema.RoleUser, "go"), code}, recencyGuards()...)
	after := append([]schema.Message{textMsg(schema.RoleUser, "go")}, recencyGuards()...)
	rej := gate.Check(before, after)
	if rej == nil || rej.Rule != "structured_integrity" {
		t.Fatalf("dropping code block must be rejected, got %v", rej)
	}
}

func TestGateStructuredIntegrityCodeFoldedPasses(t *testing.T) {
	gate := DefaultFidelityGate()
	src := "```python\ndef f():\n    return 1\n\n\n\n```"
	folded := FoldStructured(src)
	if folded == src {
		t.Fatalf("fixture must actually fold")
	}
	before := append([]schema.Message{textMsg(schema.RoleUser, src)}, recencyGuards()...)
	after := append([]schema.Message{textMsg(schema.RoleUser, folded)}, recencyGuards()...)
	if rej := gate.Check(before, after); rej != nil {
		t.Fatalf("folded code block must pass, got %v", rej)
	}
}

func TestGateStructuredIntegrityCodeRewrittenRejected(t *testing.T) {
	gate := DefaultFidelityGate()
	src := "```python\ndef f():\n    return 1\n```"
	rewritten := strings.ReplaceAll(src, "return 1", "return 2")
	before := append([]schema.Message{textMsg(schema.RoleUser, src), textMsg(schema.RoleUser, "go")}, recencyGuards()...)
	after := append([]schema.Message{textMsg(schema.RoleUser, rewritten), textMsg(schema.RoleUser, "go")}, recencyGuards()...)
	rej := gate.Check(before, after)
	if rej == nil || rej.Rule != "structured_integrity" {
		t.Fatalf("rewriting code content must be rejected, got %v", rej)
	}
}

func TestGateStructuredIntegrityProseOnlyPasses(t *testing.T) {
	gate := DefaultFidelityGate()
	before := append([]schema.Message{textMsg(schema.RoleUser, strings.Repeat("plain words ", 30))}, recencyGuards()...)
	after := append([]schema.Message{textMsg(schema.RoleUser, "short")}, recencyGuards()...)
	if rej := gate.Check(before, after); rej != nil {
		t.Fatalf("prose-only must not be affected by structured_integrity, got %v", rej)
	}
}

func TestFoldingPipelineEndToEnd(t *testing.T) {
	p := NewPipeline(nil, NewFoldingStage(FoldingConfig{MinTokens: 1, MinTextChars: 1, RecencyGuard: 2}))
	msgs := foldingFixture()
	out, stats := p.Run(NewStageContext("m", "", msgs), msgs)
	if len(stats) != 1 || !stats[0].Applied {
		t.Fatalf("folding stage must be applied: %+v", stats)
	}
	if stats[0].Saved <= 0 {
		t.Fatalf("folding must save tokens, saved=%d", stats[0].Saved)
	}
	rej := DefaultFidelityGate().Check(msgs, out)
	if rej != nil {
		t.Fatalf("folding output must pass the default gate, got %v", rej)
	}
}

func TestBuildComboFoldingRegistered(t *testing.T) {
	p, err := BuildCombo([]string{"folding"})
	if err != nil {
		t.Fatalf("folding must be buildable in combos: %v", err)
	}
	if names := p.StageNames(); len(names) != 1 || names[0] != "folding" {
		t.Fatalf("unexpected stage names %v", names)
	}
}

func mustCanonical(t *testing.T, s string) string {
	t.Helper()
	c, ok := canonicalJSON(s)
	if !ok {
		t.Fatalf("fixture must be valid JSON")
	}
	return c
}
