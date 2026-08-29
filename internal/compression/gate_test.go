package compression

import (
	"errors"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

func textMsg(role, text string) schema.Message {
	return schema.Message{Role: role, Content: schema.NewTextContent(text)}
}

func fixtureMessages() []schema.Message {
	return []schema.Message{
		textMsg(schema.RoleSystem, "You are helpful."),
		textMsg(schema.RoleUser, strings.Repeat("long history ", 50)),
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: schema.ToolCallFunction{
				Name:      "get_weather",
				Arguments: `{"city":"SF"}`,
			},
		}}},
		{Role: schema.RoleTool, ToolCallID: "call_1",
			Content: schema.NewTextContent(strings.Repeat("weather output ", 40))},
		textMsg(schema.RoleUser, "Summarize"),
	}
}

func TestMessagesNonEmpty(t *testing.T) {
	base := fixtureMessages()
	if err := messagesNonEmpty(base, base); err != nil {
		t.Fatalf("identical pass: %v", err)
	}
	if err := messagesNonEmpty(base, nil); err == nil {
		t.Fatal("emptied list must be rejected")
	}
	shell := append([]schema.Message{}, base...)
	shell[1] = schema.Message{Role: schema.RoleUser}
	if err := messagesNonEmpty(base, shell); err == nil {
		t.Fatal("empty-shell message must be rejected")
	}
}

func TestSystemPreserved(t *testing.T) {
	base := fixtureMessages()
	if err := systemPreserved(base, base); err != nil {
		t.Fatalf("identical pass: %v", err)
	}
	dropped := base[1:]
	if err := systemPreserved(base, dropped); err == nil {
		t.Fatal("dropped system must be rejected")
	}
	altered := append([]schema.Message{}, base...)
	altered[0] = textMsg(schema.RoleSystem, "You are terse.")
	if err := systemPreserved(base, altered); err == nil {
		t.Fatal("altered system text must be rejected")
	}
	// 允许 system 文本复制的场景：before 1 条，after 同文 2 条 → 通过。
}

func TestToolCallsPreserved(t *testing.T) {
	base := fixtureMessages()
	if err := toolCallsPreserved(base, base); err != nil {
		t.Fatalf("identical pass: %v", err)
	}
	noCalls := append([]schema.Message{}, base...)
	noCalls[2] = schema.Message{Role: schema.RoleAssistant}
	if err := toolCallsPreserved(base, noCalls); err == nil {
		t.Fatal("dropped tool_call must be rejected")
	}
	renamed := append([]schema.Message{}, base...)
	renamed[2].ToolCalls = []schema.ToolCall{{
		ID: "call_1", Type: "function",
		Function: schema.ToolCallFunction{Name: "other_tool"},
	}}
	if err := toolCallsPreserved(base, renamed); err == nil {
		t.Fatal("renamed tool_call must be rejected")
	}
}

func TestToolResultsPreserved(t *testing.T) {
	base := fixtureMessages()
	if err := toolResultsPreserved(base, base); err != nil {
		t.Fatalf("identical pass: %v", err)
	}
	dropped := append([]schema.Message{}, base[:3]...)
	dropped = append(dropped, base[4])
	if err := toolResultsPreserved(base, dropped); err == nil {
		t.Fatal("dropped tool result must be rejected")
	}
	// 文本变短、结构保留 → 通过（给 4.3 ToolOutputFilter 留空间）。
	shortened := append([]schema.Message{}, base...)
	shortened[3] = schema.Message{Role: schema.RoleTool, ToolCallID: "call_1",
		Content: schema.NewTextContent("ok")}
	if err := toolResultsPreserved(base, shortened); err != nil {
		t.Fatalf("shortened tool text must pass: %v", err)
	}
}

func TestRecencyRule(t *testing.T) {
	base := fixtureMessages()
	rule := RecencyRule(2)
	if err := rule(base, base); err != nil {
		t.Fatalf("identical pass: %v", err)
	}
	mutated := append([]schema.Message{}, base...)
	mutated[4] = textMsg(schema.RoleUser, "Summarize!")
	if err := rule(base, mutated); err == nil {
		t.Fatal("modified tail message must be rejected")
	}
	mutated[3] = textMsg(schema.RoleTool, "changed")
	mutated[3].ToolCallID = "call_1"
	if err := rule(base, mutated); err == nil {
		t.Fatal("modified penultimate message must be rejected")
	}
	// 历史消息（窗口外）可改 → 通过。
	early := append([]schema.Message{}, base...)
	early[1] = textMsg(schema.RoleUser, "short")
	if err := rule(base, early); err != nil {
		t.Fatalf("out-of-window edit must pass: %v", err)
	}
	if err := RecencyRule(0)(base, base); err != nil {
		t.Fatalf("window<=0 clamps to 1, identical pass: %v", err)
	}
}

func TestDefaultFidelityGateCheck(t *testing.T) {
	gate := DefaultFidelityGate()
	base := fixtureMessages()
	if rej := gate.Check(base, base); rej != nil {
		t.Fatalf("identical pass: %v", rej)
	}
	dropped := base[1:]
	rej := gate.Check(base, dropped)
	if rej == nil || rej.Rule != "system_preserved" {
		t.Fatalf("want system_preserved rejection, got %+v", rej)
	}
	var target *GateRejection
	if !errors.As(rej, &target) || target.Rule != "system_preserved" {
		t.Fatal("rejection must unwrap to *GateRejection")
	}
}

func TestEstimateTokens(t *testing.T) {
	msgs := []schema.Message{
		textMsg(schema.RoleSystem, "ab"), // 4 + 2/4 = 4
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{
			ID: "c1", Type: "function",
			Function: schema.ToolCallFunction{Name: "get_weather", Arguments: `{"a":1}`},
		}}}, // 4 + 0 + (11+7)/4 = 8
	}
	if got := EstimateTokens(msgs); got != 12 {
		t.Fatalf("EstimateTokens = %d, want 12", got)
	}
	if got := EstimateTokens(nil); got != 0 {
		t.Fatalf("nil = %d, want 0", got)
	}
}

func TestNewStageContext(t *testing.T) {
	msgs := fixtureMessages()
	sc := NewStageContext("gpt-x", "sess-1", msgs)
	if sc.Model != "gpt-x" || sc.SessionID != "sess-1" {
		t.Fatalf("context fields: %+v", sc)
	}
	if sc.MessageCount != len(msgs) {
		t.Fatalf("MessageCount = %d, want %d", sc.MessageCount, len(msgs))
	}
	if sc.EstimatedTokens != EstimateTokens(msgs) {
		t.Fatal("EstimatedTokens mismatch with EstimateTokens")
	}
}
