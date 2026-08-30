package compression

import (
	"fmt"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// toolSession 构造一条在 guard 窗口外的长工具输出会话。
func toolSession(command, output string) []schema.Message {
	return []schema.Message{
		textMsg(schema.RoleSystem, "You are an agent."),
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{
			ID: "c1", Type: "function",
			Function: schema.ToolCallFunction{
				Name:      "run_command",
				Arguments: `{"command":"` + command + `"}`,
			},
		}}},
		{Role: schema.RoleTool, ToolCallID: "c1", Content: schema.NewTextContent(output)},
		textMsg(schema.RoleAssistant, "Noted."),
		textMsg(schema.RoleUser, "Continue."),
	}
}

// sampleOutputs 是验收样例集：三类典型终端输出。
func sampleOutputs() []struct {
	name    string
	command string
	text    string
} {
	var files []string
	for i := 0; i < 500; i++ {
		files = append(files, fmt.Sprintf("libmodule-v2.%d.0-x86_64-linux-gnu.so", i))
	}
	var build []string
	for i := 0; i < 10; i++ {
		build = append(build, fmt.Sprintf("compiling package %d of 200", i))
	}
	for i := 0; i < 170; i++ {
		build = append(build, "warning: unused variable x")
	}
	for i := 0; i < 20; i++ {
		build = append(build, fmt.Sprintf("linking binary step %d done", i))
	}
	var watch []string
	for i := 0; i < 100; i++ {
		watch = append(watch, "cpu 45%  mem 1.2G  net 3.4MB/s")
	}
	return []struct {
		name    string
		command string
		text    string
	}{
		{"directory listing", "ls -la /usr/lib", strings.Join(files, "\n")},
		{"build log", "go build ./...", strings.Join(build, "\n")},
		{"monitoring loop", "watch -n1 stats", strings.Join(watch, "\n")},
	}
}

// TestToolFilterSampleSuite 是 4.3 的验收用例：样例集内
// 长终端输出的字符压缩率全部 ≥ 50%。
func TestToolFilterSampleSuite(t *testing.T) {
	st := NewToolFilterStage(ToolFilterConfig{
		MinTokens: 1, MinOutputChars: 100, MaxLines: 120,
		HeadLines: 40, TailLines: 20, RecencyGuard: 2,
	})
	for _, s := range sampleOutputs() {
		t.Run(s.name, func(t *testing.T) {
			in := toolSession(s.command, s.text)
			out, stats, err := st.Apply(in)
			if err != nil {
				t.Fatalf("Apply error: %v", err)
			}
			folded := singleTextOf(out[2])
			ratio := 1 - float64(len(folded))/float64(len(s.text))
			if ratio < 0.5 {
				t.Fatalf("compression ratio %.2f < 0.50 (folded %d of %d chars)",
					ratio, len(folded), len(s.text))
			}
			if !stats.Applied || stats.Saved <= 0 {
				t.Fatalf("stats = %+v", stats)
			}
		})
	}
}

func TestToolFilterKeepsStructure(t *testing.T) {
	in := toolSession("ls -la /usr/lib", strings.Repeat("same line\n", 300))
	st := NewToolFilterStage(ToolFilterConfig{MinTokens: 1, RecencyGuard: 2})
	out, _, err := st.Apply(in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("message count changed: %d", len(out))
	}
	if out[2].Role != schema.RoleTool || out[2].ToolCallID != "c1" {
		t.Fatalf("tool identity lost: %+v", out[2])
	}
	if out[1].ToolCalls[0].ID != "c1" {
		t.Fatal("assistant tool_call must stay intact")
	}
}

func TestToolFilterRespectsRecencyGuard(t *testing.T) {
	long := strings.Repeat("line\n", 300)
	in := []schema.Message{
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{
			ID: "c1", Type: "function",
			Function: schema.ToolCallFunction{Name: "run_command", Arguments: `{"command":"cat x"}`},
		}}},
		textMsg(schema.RoleAssistant, "previous"),
		{Role: schema.RoleTool, ToolCallID: "c1", Content: schema.NewTextContent(long)},
		textMsg(schema.RoleUser, "next"),
	}
	out, _, err := NewToolFilterStage(ToolFilterConfig{MinTokens: 1, RecencyGuard: 2}).Apply(in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if singleTextOf(out[2]) != long {
		t.Fatal("recency-guarded tool output must stay untouched")
	}
}

func TestToolFilterSkipsShortOutput(t *testing.T) {
	in := toolSession("cat small.txt", "just a few lines\n")
	st := NewToolFilterStage(ToolFilterConfig{
		MinTokens: 1, MinOutputChars: 500, RecencyGuard: 2,
	})
	out, stats, err := st.Apply(in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if singleTextOf(out[2]) != "just a few lines\n" || stats.Saved != 0 {
		t.Fatalf("short output must be untouched, stats = %+v", stats)
	}
}

// TestToolFilterCommandAware 验证命令感知：列表类保头不保尾，
// 日志类头尾都保。
func TestToolFilterCommandAware(t *testing.T) {
	cfg := ToolFilterConfig{MinTokens: 1, MinOutputChars: 100,
		MaxLines: 120, HeadLines: 10, TailLines: 5, RecencyGuard: 2}
	st := NewToolFilterStage(cfg)
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, fmt.Sprintf("entry-%03d", i))
	}
	text := strings.Join(lines, "\n")
	last := "entry-199"

	listOut, _, _ := st.Apply(toolSession("ls -la /big", text))
	listFolded := singleTextOf(listOut[2])
	if strings.Contains(listFolded, last) {
		t.Fatal("list command must not keep the tail")
	}
	if !strings.Contains(listFolded, "entry-000") {
		t.Fatal("list command must keep the head")
	}

	logOut, _, _ := st.Apply(toolSession("go test ./...", text))
	logFolded := singleTextOf(logOut[2])
	if !strings.Contains(logFolded, last) || !strings.Contains(logFolded, "entry-000") {
		t.Fatal("log command must keep both head and tail")
	}
}

func TestToolFilterTotalsAndShouldRun(t *testing.T) {
	st := NewToolFilterStage(ToolFilterConfig{MinTokens: 1000, RecencyGuard: 2})
	if st.ShouldRun(NewStageContext("m", "s", toolSession("ls", "x"))) {
		t.Fatal("below MinTokens must not run")
	}
	st2 := NewToolFilterStage(ToolFilterConfig{
		MinTokens: 1, MinOutputChars: 100, RecencyGuard: 2,
	})
	for _, s := range sampleOutputs() {
		if _, _, err := st2.Apply(toolSession(s.command, s.text)); err != nil {
			t.Fatalf("Apply error: %v", err)
		}
	}
	tot := st2.Totals()
	if tot.OutputsFiltered != 3 || tot.Rate() < 0.5 {
		t.Fatalf("totals = %+v rate = %f", tot, tot.Rate())
	}
}

func TestToolFilterPipelineIntegration(t *testing.T) {
	s := sampleOutputs()[0]
	in := toolSession(s.command, s.text)
	p := NewPipeline(nil, NewToolFilterStage(ToolFilterConfig{
		MinTokens: 1, MinOutputChars: 100, RecencyGuard: 2,
	}))
	out, stats := p.Run(NewStageContext("m", "sess", in), in)
	if len(stats) != 1 || stats[0].GateRejected != nil || !stats[0].Applied {
		t.Fatalf("tool filter must pass the fidelity gate, stats = %+v", stats)
	}
	if len(singleTextOf(out[2])) >= len(s.text) {
		t.Fatal("expected folded output")
	}
}

func TestCollapseAndTruncate(t *testing.T) {
	repeated := "a\na\na\nb"
	if got := collapseRepeatedLines(repeated); got != "a\n[3 identical lines folded]\nb" {
		t.Fatalf("collapse = %q", got)
	}
	short := "1\n2\n3"
	if got := truncateLines(short, 2, 2); got != short {
		t.Fatalf("short text must stay: %q", got)
	}
	long := strings.Repeat("x\n", 10) // split 后含尾空行共 11 元素
	if got := truncateLines(long, 2, 2); !strings.Contains(got, "[7 lines elided]") {
		t.Fatalf("truncate = %q", got)
	}
}
