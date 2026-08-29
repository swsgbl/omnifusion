package compression

import (
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// cavemanSamples 是验收样例集：冗长请求、粘贴文档、简洁消息。
func cavemanSamples() []struct {
	name  string
	text  string
	minOK float64 // 样例的最低可接受节省率
} {
	verbose := strings.Repeat(
		"In order to proceed, please basically review the document very carefully. "+
			"It is important to note that, due to the fact that the deadline is quite near, "+
			"we should actually start now.  ", 6)
	pasted := strings.Repeat(
		"    title:      configuration\n\n\n"+
			"    value:   1\n\n"+
			"    note:     see   docs    \n\n\n\n", 8)
	concise := "Fix the null check in handleRequest."
	return []struct {
		name  string
		text  string
		minOK float64
	}{
		{"verbose request", verbose, 0.20},
		{"pasted document", pasted, 0.25},
		{"concise message", concise, 0},
	}
}

// TestCavemanSampleSuite 是 docs/05 4.4 的验收：样例集节省率报告
// （t.Logf 输出报告），冗长样例达标、任何样例不膨胀。
func TestCavemanSampleSuite(t *testing.T) {
	for _, s := range cavemanSamples() {
		t.Run(s.name, func(t *testing.T) {
			got := cavemanText(s.text)
			rate := 1 - float64(len(got))/float64(len(s.text))
			t.Logf("sample %-16s before=%6d after=%6d saved=%.1f%%",
				s.name, len(s.text), len(got), rate*100)
			if rate < s.minOK {
				t.Fatalf("savings rate %.1f%% < %.1f%%", rate*100, s.minOK*100)
			}
			if rate < 0 {
				t.Fatalf("output grew: %d -> %d", len(s.text), len(got))
			}
		})
	}
}

func TestCavemanTextRules(t *testing.T) {
	if got := cavemanText("In order to proceed"); got != "to proceed" {
		t.Fatalf("phrase rule: %q", got)
	}
	if got := cavemanText("please basically do it"); got != "do it" {
		t.Fatalf("filler rule: %q", got)
	}
	if got := cavemanText("a    b\t\tc"); got != "a b c" {
		t.Fatalf("whitespace rule: %q", got)
	}
	if got := cavemanText("line\n\n\n\nline2"); got != "line\n\nline2" {
		t.Fatalf("blank-line rule: %q", got)
	}
	if got := cavemanText("Justified"); got != "Justified" {
		t.Fatalf("word boundary must hold: %q", got)
	}
}

// cavemanSession 构造含一条可压缩长文本（guard 外）的会话。
func cavemanSession(text string) []schema.Message {
	return []schema.Message{
		textMsg(schema.RoleSystem, "You are helpful."),
		textMsg(schema.RoleUser, text),
		textMsg(schema.RoleAssistant, "Understood."),
		textMsg(schema.RoleUser, "Continue."),
	}
}

func TestCavemanStageApplies(t *testing.T) {
	s := cavemanSamples()[0]
	in := cavemanSession(s.text)
	st := NewCavemanStage(CavemanConfig{
		MinTokens: 1, MinTextChars: 100, RecencyGuard: 2,
	})
	out, stats, err := st.Apply(in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if !stats.Applied || stats.Saved <= 0 {
		t.Fatalf("stats = %+v", stats)
	}
	if len(singleTextOf(out[1])) >= len(s.text) {
		t.Fatal("expected shortened text")
	}
	// system 与结构消息不动。
	if singleTextOf(out[0]) != "You are helpful." {
		t.Fatal("system must stay untouched")
	}
}

func TestCavemanRespectsGuardAndThresholds(t *testing.T) {
	long := strings.Repeat("In order to please proceed ", 40)
	in := []schema.Message{
		textMsg(schema.RoleUser, long),
		textMsg(schema.RoleUser, long), // 尾部 guard 内：不动
	}
	st := NewCavemanStage(CavemanConfig{MinTokens: 1, MinTextChars: 100, RecencyGuard: 1})
	out, _, err := st.Apply(in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if singleTextOf(out[1]) != long {
		t.Fatal("guarded tail must stay untouched")
	}
	if singleTextOf(out[0]) == long {
		t.Fatal("out-of-window text must be rewritten")
	}
	// 短文本低于阈值：整条不动。
	short := cavemanSession("short")
	st2 := NewCavemanStage(CavemanConfig{MinTokens: 1, MinTextChars: 100, RecencyGuard: 2})
	out2, stats2, err := st2.Apply(short)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if singleTextOf(out2[1]) != "short" || stats2.Saved != 0 {
		t.Fatalf("short text must be untouched, stats = %+v", stats2)
	}
}

func TestCavemanTotalsAndShouldRun(t *testing.T) {
	st := NewCavemanStage(CavemanConfig{MinTokens: 500, RecencyGuard: 2})
	if st.ShouldRun(NewStageContext("m", "s", cavemanSession("x"))) {
		t.Fatal("below MinTokens must not run")
	}
	st2 := NewCavemanStage(CavemanConfig{MinTokens: 1, MinTextChars: 100, RecencyGuard: 2})
	for _, s := range cavemanSamples() {
		if _, _, err := st2.Apply(cavemanSession(s.text)); err != nil {
			t.Fatalf("Apply error: %v", err)
		}
	}
	tot := st2.Totals()
	if tot.Runs != 3 || tot.Rate() <= 0 || tot.Rate() >= 1 {
		t.Fatalf("totals = %+v", tot)
	}
}

func TestCavemanPipelineIntegration(t *testing.T) {
	s := cavemanSamples()[0]
	in := cavemanSession(s.text)
	p := NewPipeline(nil,
		NewDedupStage(DedupConfig{MinTokens: 1, RecencyGuard: 2}),
		NewCavemanStage(CavemanConfig{MinTokens: 1, MinTextChars: 100, RecencyGuard: 2}))
	out, stats := p.Run(NewStageContext("m", "sess", in), in)
	if len(stats) != 2 {
		t.Fatalf("two stages expected, stats = %+v", stats)
	}
	for i, s := range stats {
		if s.GateRejected != nil || s.Err != nil {
			t.Fatalf("stage %d must pass gate: %+v", i, s)
		}
	}
	if len(singleTextOf(out[1])) >= len(s.text) {
		t.Fatal("expected compressed output")
	}
}
