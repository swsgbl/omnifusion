// report_test.go 锁死 FidelityReport 契约：采纳阶段证据完整（结构
// 值全覆盖/工具参数一致/system 完整/压缩比<1）、拦截阶段带失败类型、
// 跳过阶段平凡证据、Summary 单行格式稳定。
package compression

import (
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

func TestRunWithReportAppliedStage(t *testing.T) {
	p := NewPipeline(nil, NewFoldingStage(FoldingConfig{MinTokens: 1, MinTextChars: 1, RecencyGuard: 2}))
	msgs := foldingFixture()
	_, stats, rep := p.RunWithReport(NewStageContext("m", "", msgs), msgs)
	if len(stats) != 1 || !stats[0].Applied {
		t.Fatalf("folding must be applied: %+v", stats)
	}
	if len(rep.Stages) != 1 {
		t.Fatalf("report must carry one stage, got %d", len(rep.Stages))
	}
	f := rep.Stages[0]
	if !f.Applied || f.Skipped || f.Rejection != "" || f.Err != "" {
		t.Fatalf("unexpected stage fidelity: %+v", f)
	}
	if f.StructuredTotal < 2 || f.StructuredKept != f.StructuredTotal {
		t.Fatalf("structured coverage must be total, got %d/%d", f.StructuredKept, f.StructuredTotal)
	}
	if !f.ToolArgsIntact || !f.SystemPreserved {
		t.Fatalf("tool args / system must be intact: %+v", f)
	}
	if f.CompressionRatio >= 1.0 {
		t.Fatalf("applied folding ratio must be < 1.0, got %f", f.CompressionRatio)
	}
}

func TestRunWithReportRejectedStage(t *testing.T) {
	p := NewPipeline(nil, droppingStage{})
	msgs := append([]schema.Message{textMsg(schema.RoleUser, prettyJSON)}, recencyGuards()...)
	// droppingStage 丢弃 JSON 消息但保住尾部 recency 带。
	msgs = append([]schema.Message{textMsg(schema.RoleSystem, "sys")}, msgs...)
	out, stats, rep := p.RunWithReport(NewStageContext("m", "", msgs), msgs)
	if stats[0].Applied || stats[0].GateRejected == nil {
		t.Fatalf("stage must be gate-rejected: %+v", stats[0])
	}
	f := rep.Stages[0]
	if f.Rejection != "structured_integrity" {
		t.Fatalf("rejection type must name the rule, got %q", f.Rejection)
	}
	if f.CompressionRatio != 1.0 {
		t.Fatalf("rejected stage must report ratio 1.0, got %f", f.CompressionRatio)
	}
	if len(out) != len(msgs) {
		t.Fatalf("rejected output must fall back to stage input")
	}
}

func TestRunWithReportSkippedStage(t *testing.T) {
	p := NewPipeline(nil, NewFoldingStage(FoldingConfig{MinTokens: 1 << 20, MinTextChars: 1, RecencyGuard: 2}))
	msgs := foldingFixture()
	_, _, rep := p.RunWithReport(NewStageContext("m", "", msgs), msgs)
	f := rep.Stages[0]
	if !f.Skipped || f.Applied {
		t.Fatalf("stage must be skipped: %+v", f)
	}
	if f.CompressionRatio != 1.0 {
		t.Fatalf("skipped stage ratio must be 1.0, got %f", f.CompressionRatio)
	}
	if !f.ToolArgsIntact || !f.SystemPreserved || f.StructuredKept != f.StructuredTotal {
		t.Fatalf("skipped stage evidence must be trivially intact: %+v", f)
	}
}

func TestFidelitySummaryFormat(t *testing.T) {
	p := NewPipeline(nil, NewFoldingStage(FoldingConfig{MinTokens: 1, MinTextChars: 1, RecencyGuard: 2}))
	msgs := foldingFixture()
	_, _, rep := p.RunWithReport(NewStageContext("m", "", msgs), msgs)
	s := rep.Summary()
	// 契约："stages=1 applied=1 struct=N/N toolargs=ok sys=ok"。
	for _, want := range []string{"stages=1 ", "applied=1 ", "toolargs=ok ", "sys=ok"} {
		if !strings.Contains(s, want) {
			t.Fatalf("summary %q missing %q", s, want)
		}
	}
	if !strings.Contains(s, "struct=2/2") && !strings.Contains(s, "struct=3/3") {
		t.Fatalf("summary %q missing full structured coverage", s)
	}
	var nilRep *FidelityReport
	if nilRep.Summary() != "stages=0" {
		t.Fatalf("nil report summary must be stages=0")
	}
}

func TestStageSummaryFormat(t *testing.T) {
	cases := map[StageFidelity]string{
		{Stage: "folding", Applied: true, CompressionRatio: 0.42}: "folding ok ratio=0.42",
		{Stage: "semantic", Skipped: true}:                        "semantic skipped ratio=0.00",
		{Stage: "caveman", Rejection: "structured_integrity"}:     "caveman rejected=structured_integrity ratio=0.00",
		{Stage: "x", Err: "boom"}:                                 "x error ratio=0.00",
	}
	for f, want := range cases {
		if got := f.Summary(); got != want {
			t.Fatalf("stage summary = %q, want %q", got, want)
		}
	}
}

func TestRunWithReportMatchesRun(t *testing.T) {
	// 证据版与原版行为必须一致（报告是纯观察面，不改变压缩结果）。
	p := NewPipeline(nil, NewDedupStage(DedupConfig{MinTokens: 1, RecencyGuard: 2}),
		NewFoldingStage(FoldingConfig{MinTokens: 1, MinTextChars: 1, RecencyGuard: 2}))
	msgs := append(foldingFixture(), textMsg(schema.RoleUser, strings.Repeat("dup body ", 10)),
		textMsg(schema.RoleUser, strings.Repeat("dup body ", 10)))
	sc := NewStageContext("m", "", msgs)
	outA, statsA := p.Run(sc, msgs)
	outB, statsB, _ := p.RunWithReport(sc, msgs)
	if len(outA) != len(outB) {
		t.Fatalf("Run and RunWithReport diverged: %d vs %d messages", len(outA), len(outB))
	}
	if len(statsA) != len(statsB) {
		t.Fatalf("stats count diverged")
	}
	for i := range statsA {
		if statsA[i] != statsB[i] {
			t.Fatalf("stats[%d] diverged: %+v vs %+v", i, statsA[i], statsB[i])
		}
	}
}
