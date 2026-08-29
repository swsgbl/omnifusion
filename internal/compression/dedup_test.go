package compression

import (
	"reflect"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// dedupFixture 是含重复轮次的会话：user 重复粘贴同一段需求两次
// （重复落在尾部 guard 窗口之外）。
func dedupFixture() []schema.Message {
	dup := strings.Repeat("please analyze the quarterly report ", 30)
	return []schema.Message{
		textMsg(schema.RoleSystem, "You are helpful."),
		textMsg(schema.RoleUser, dup),
		textMsg(schema.RoleAssistant, "First analysis pass."),
		textMsg(schema.RoleUser, dup), // 重复轮次（guard 外）
		textMsg(schema.RoleAssistant, "Second analysis pass."),
		textMsg(schema.RoleUser, "Now summarize."),
	}
}

func TestDedupRemovesDuplicateTurns(t *testing.T) {
	in := dedupFixture()
	st := NewDedupStage(DefaultDedupConfig())
	out, stats, err := st.Apply(in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if len(out) != len(in)-1 {
		t.Fatalf("output len = %d, want %d", len(out), len(in)-1)
	}
	if !stats.Applied || stats.Saved <= 0 {
		t.Fatalf("stats = %+v", stats)
	}
	// 保留首次出现（index 1），重复的 index 3 被折叠。
	if out[1].Content.TextOf() != in[1].Content.TextOf() {
		t.Fatal("must keep the first occurrence")
	}
	if out[3].Content.TextOf() != in[4].Content.TextOf() {
		t.Fatal("subsequent messages must shift up intact")
	}
	// 纯函数纪律：输入切片不可被修改。
	if !reflect.DeepEqual(in, dedupFixture()) {
		t.Fatal("input slice must not be mutated")
	}
}

func TestDedupKeepsStructuralMessages(t *testing.T) {
	same := strings.Repeat("identical payload ", 40)
	in := []schema.Message{
		textMsg(schema.RoleSystem, same),
		textMsg(schema.RoleSystem, same), // system 重复：不参与去重
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{
			ID: "c1", Type: "function",
			Function: schema.ToolCallFunction{Name: "f", Arguments: "{}"},
		}}},
		{Role: schema.RoleTool, ToolCallID: "c1", Content: schema.NewTextContent(same)},
		{Role: schema.RoleTool, ToolCallID: "c1b", Content: schema.NewTextContent(same)},
		textMsg(schema.RoleUser, "go"),
	}
	st := NewDedupStage(DefaultDedupConfig())
	out, _, err := st.Apply(in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("structural messages must be kept, len = %d", len(out))
	}
}

func TestDedupRespectsRecencyGuard(t *testing.T) {
	dup := strings.Repeat("repeat me ", 50)
	in := []schema.Message{
		textMsg(schema.RoleUser, dup),
		textMsg(schema.RoleAssistant, "ok"),
		textMsg(schema.RoleUser, dup), // 落在尾部 guard 内：保留
	}
	st := NewDedupStage(DedupConfig{MinTokens: 1, RecencyGuard: 2})
	out, stats, err := st.Apply(in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if len(out) != len(in) || stats.Saved != 0 {
		t.Fatalf("recency-guarded duplicate must stay: len=%d stats=%+v", len(out), stats)
	}
}

func TestDedupShouldRun(t *testing.T) {
	st := NewDedupStage(DedupConfig{MinTokens: 100, RecencyGuard: 2})
	if st.ShouldRun(NewStageContext("m", "s", []schema.Message{textMsg("user", "hi")})) {
		t.Fatal("tiny context must not run")
	}
	big := []schema.Message{
		textMsg(schema.RoleUser, strings.Repeat("x", 500)),
		textMsg(schema.RoleAssistant, strings.Repeat("y", 500)),
		textMsg(schema.RoleUser, strings.Repeat("z", 500)),
	}
	if !st.ShouldRun(NewStageContext("m", "s", big)) {
		t.Fatal("large context must run")
	}
}

func TestDedupTotalsAccumulate(t *testing.T) {
	st := NewDedupStage(DedupConfig{MinTokens: 1, RecencyGuard: 2})
	for i := 0; i < 2; i++ {
		if _, _, err := st.Apply(dedupFixture()); err != nil {
			t.Fatalf("Apply %d error: %v", i, err)
		}
	}
	tot := st.Totals()
	if tot.Runs != 2 || tot.MessagesRemoved != 2 || tot.TokensSaved <= 0 {
		t.Fatalf("totals = %+v", tot)
	}
	// 去重率统计可见：0 < rate < 1 且与单次 stats 一致。
	if tot.Rate() <= 0 || tot.Rate() >= 1 {
		t.Fatalf("dedup rate out of range: %f", tot.Rate())
	}
}

func TestDedupPipelineIntegration(t *testing.T) {
	in := dedupFixture()
	p := NewPipeline(nil, NewDedupStage(DedupConfig{MinTokens: 1, RecencyGuard: 2}))
	out, stats := p.Run(NewStageContext("m", "sess", in), in)
	if len(stats) != 1 || !stats[0].Applied || stats[0].GateRejected != nil {
		t.Fatalf("dedup must pass the fidelity gate, stats = %+v", stats)
	}
	if len(out) != len(in)-1 || stats[0].Saved <= 0 {
		t.Fatalf("expected one duplicate removed, out=%d stats=%+v", len(out), stats[0])
	}
}
