package compression

import (
	"reflect"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// stubStage 是测试用阶段桩：固定 Name/ShouldRun，Apply 委托闭包。
type stubStage struct {
	name      string
	shouldRun bool
	apply     func(msgs []schema.Message) ([]schema.Message, error)
	calls     int
}

func (s *stubStage) Name() string                    { return s.name }
func (s *stubStage) ShouldRun(sc *StageContext) bool { return s.shouldRun }
func (s *stubStage) Apply(msgs []schema.Message) ([]schema.Message, CompressionStats, error) {
	s.calls++
	out, err := s.apply(msgs)
	return out, CompressionStats{Stage: s.name}, err
}

// shortenHistory 是"健康"压缩：把窗口外的长历史缩短，保全部骨架。
func shortenHistory(msgs []schema.Message) []schema.Message {
	out := append([]schema.Message{}, msgs...)
	for i := range out {
		if i == 1 { // 窗口外的长 user 历史
			out[i] = textMsg(schema.RoleUser, "history summary.")
		}
	}
	return out
}

// healthyApply 把 shortenHistory 适配成 stubStage 的 Apply 形状。
func healthyApply(msgs []schema.Message) ([]schema.Message, error) {
	return shortenHistory(msgs), nil
}

func TestPipelineAppliesHealthyStage(t *testing.T) {
	in := fixtureMessages()
	st := &stubStage{name: "summarizer", shouldRun: true,
		apply: healthyApply}
	p := NewPipeline(nil, st)
	out, stats := p.Run(NewStageContext("m", "s", in), in)
	if len(stats) != 1 || !stats[0].Applied || stats[0].Skipped {
		t.Fatalf("stats = %+v", stats)
	}
	if stats[0].Saved <= 0 || stats[0].AfterTokens >= stats[0].BeforeTokens {
		t.Fatalf("expected token savings, stats = %+v", stats[0])
	}
	if reflect.DeepEqual(out, in) {
		t.Fatal("healthy stage output must differ from input")
	}
	if st.calls != 1 {
		t.Fatalf("Apply calls = %d, want 1", st.calls)
	}
}

func TestPipelineSkipsByShouldRun(t *testing.T) {
	in := fixtureMessages()
	st := &stubStage{name: "off", shouldRun: false, apply: healthyApply}
	out, stats := NewPipeline(nil, st).Run(NewStageContext("m", "", in), in)
	if !stats[0].Skipped || stats[0].Applied || stats[0].GateRejected != nil {
		t.Fatalf("stats = %+v", stats)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatal("skipped stage must pass input through")
	}
	if st.calls != 0 {
		t.Fatalf("Apply must not run, calls = %d", st.calls)
	}
}

func TestPipelineStageErrorFallsBack(t *testing.T) {
	in := fixtureMessages()
	st := &stubStage{name: "broken", shouldRun: true,
		apply: func(msgs []schema.Message) ([]schema.Message, error) {
			return nil, errString("upstream summary failed")
		}}
	out, stats := NewPipeline(nil, st).Run(NewStageContext("m", "", in), in)
	if stats[0].Err == nil || stats[0].Applied {
		t.Fatalf("stats = %+v", stats)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatal("failed stage must fall back to input")
	}
}

func TestPipelineStagePanicRecovers(t *testing.T) {
	in := fixtureMessages()
	st := &stubStage{name: "panicky", shouldRun: true,
		apply: func(msgs []schema.Message) ([]schema.Message, error) {
			panic("boom")
		}}
	out, stats := NewPipeline(nil, st).Run(NewStageContext("m", "", in), in)
	if stats[0].Err == nil || !strings.Contains(stats[0].Err.Error(), "panicked") {
		t.Fatalf("panic must surface as stats.Err, got %+v", stats[0])
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatal("panicking stage must fall back to input")
	}
}

// TestPipelineGateRejectsLossyStage 是 docs/05 4.1 的验收用例：
// 三种劣化压缩（删 tool 结果 / 删 system / 动 recency 窗口）都必须
// 被拦截，最终输出与输入完全一致。
func TestPipelineGateRejectsLossyStage(t *testing.T) {
	dropTool := func(msgs []schema.Message) ([]schema.Message, error) {
		var out []schema.Message
		for _, m := range msgs {
			if m.Role != schema.RoleTool {
				out = append(out, m)
			}
		}
		return out, nil
	}
	dropSystem := func(msgs []schema.Message) ([]schema.Message, error) {
		var out []schema.Message
		for _, m := range msgs {
			if m.Role != schema.RoleSystem {
				out = append(out, m)
			}
		}
		return out, nil
	}
	mutateRecency := func(msgs []schema.Message) ([]schema.Message, error) {
		out := append([]schema.Message{}, msgs...)
		out[len(out)-1] = textMsg(schema.RoleUser, "changed")
		return out, nil
	}
	cases := []struct {
		name  string
		rule  string
		apply func([]schema.Message) ([]schema.Message, error)
	}{
		{"drop tool results", "tool_results_preserved", dropTool},
		{"drop system", "system_preserved", dropSystem},
		{"mutate recency tail", "recency_preserved", mutateRecency},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := fixtureMessages()
			st := &stubStage{name: "lossy", shouldRun: true, apply: tc.apply}
			out, stats := NewPipeline(nil, st).Run(NewStageContext("m", "", in), in)
			if stats[0].Applied || stats[0].GateRejected == nil {
				t.Fatalf("lossy stage must be gate-rejected, stats = %+v", stats)
			}
			rej, ok := stats[0].GateRejected.(*GateRejection)
			if !ok || rej.Rule != tc.rule {
				t.Fatalf("want rule %s, got %+v", tc.rule, stats[0].GateRejected)
			}
			if stats[0].Err != nil {
				t.Fatalf("gate rejection is not a stage error: %v", stats[0].Err)
			}
			if !reflect.DeepEqual(out, in) {
				t.Fatal("gate-rejected output must equal input exactly")
			}
		})
	}
}

func TestPipelineMultiStageChain(t *testing.T) {
	in := fixtureMessages()
	healthy := &stubStage{name: "healthy", shouldRun: true, apply: healthyApply}
	lossy := &stubStage{name: "lossy", shouldRun: true,
		apply: func(msgs []schema.Message) ([]schema.Message, error) {
			return msgs[1:], nil // 丢 system
		}}
	out, stats := NewPipeline(nil, healthy, lossy).Run(NewStageContext("m", "", in), in)
	if !stats[0].Applied || stats[1].GateRejected == nil {
		t.Fatalf("stats = %+v", stats)
	}
	want := shortenHistory(in)
	if !reflect.DeepEqual(out, want) {
		t.Fatal("final output must be the healthy stage's product")
	}
}
