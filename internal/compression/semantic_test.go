package compression

import (
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// semanticSamples 是 6.2 的验收样例集：英文长文档、中文
// 部署文档、中英混合带代码块——多语言覆盖；每样例附带关键保真
// 断言（实体/数字/首末句/代码块必须在产出中存活）。
func semanticSamples() []struct {
	name     string
	text     string
	minOK    float64 // 样例的最低可接受节省率
	mustKeep []string
} {
	en := strings.Join([]string{
		"Deploy release ACME-7701 to the production cluster before Friday noon.",
		"The team has discussed this plan many times and we all think it is a good plan that should work well.",
		"There are many details to consider and we should consider them carefully together as a team in the meeting.",
		"The rollback plan is documented in runbook RB-2291 and takes about four minutes to execute.",
		"Everyone should read the document and share their thoughts about the document with the rest of the team.",
		"We need to make sure the schedule is clear and the schedule is shared so everyone knows the schedule.",
		"The database migration script lives in migrations/V024__add_orders.py and must run before the deploy.",
		"It is important to keep the process simple and to avoid making the process more complex than needed.",
		"Please double check the config values for the staging and production environments before you proceed.",
		"The monitoring dashboard shows the error rate and latency for each region after every single deploy.",
		"We will hold a short review meeting next week to go over what went well and what could be better.",
		"Ping the on-call engineer once the deploy finishes so they can watch the dashboards for anomalies.",
	}, " ")
	zh := strings.Join([]string{
		"服务版本 2.4.1 已通过预发环境验证，可以进入正式发布流程。",
		"这个方案我们已经讨论过很多次了，大家觉得整体思路是可行的，没有大的问题。",
		"相关的细节问题还需要再确认一下，确认之后我们再同步给相关的同事。",
		"数据库迁移脚本位于 migrations/V024__add_orders.py，必须在发布前执行完毕。",
		"发布窗口是本周四凌晨两点到四点，期间会有大约十分钟的服务不可用。",
		"回滚方案在运维手册 2291 页，执行回滚大约需要四分钟的时间。",
		"大家在发布前要把文档再读一遍，读完之后把自己的意见同步给小组。",
		"监控面板会显示每个区域的错误率和延迟，发布之后要重点观察。",
		"如果出现问题请立即联系值班工程师，值班表在共享文档里可以查到。",
		"这次发布的范围包括订单服务和库存服务，其他服务不在本次范围内。",
		"整个过程要保持简单，不要把流程搞得比需要的更复杂更繁琐。",
		"请在发布窗口结束前完成确认，确认后回复本邮件即可。",
	}, "")
	mixed := "Here is the fix for issue OF-1024.\n" +
		"The bug was reported by three users last week and it affects the checkout flow badly.\n" +
		"修复方法是校验输入长度，超过限制时直接拒绝请求并记录日志。\n" +
		"The patch changes the validation layer as shown below.\n" +
		"```go\nfunc validate(n int) error {\n\tif n > 512 {\n\t\treturn fmt.Errorf(\"too large\")\n\t}\n\treturn nil\n}\n```\n" +
		"After the change the test suite passes and the latency stays flat.\n" +
		"旧版本的行为保持不变，只有超限请求会受到影响。\n" +
		"We plan to roll this out gradually to ten percent of traffic first.\n" +
		"We should also thank everyone who helped triage the issue and kept the impact small.\n" +
		"回顾这次排障过程，团队协作与快速响应是解决问题的关键所在，值得肯定。\n" +
		"如有问题请随时联系值班同学，感谢大家的支持与配合。\n"
	return []struct {
		name     string
		text     string
		minOK    float64
		mustKeep []string
	}{
		{"english doc", en, 0.15, []string{"ACME-7701", "RB-2291", "migrations/V024", "Ping the on-call engineer"}},
		{"chinese doc", zh, 0.15, []string{"2.4.1", "V024", "2291", "发布窗口结束前完成确认"}},
		{"mixed with code", mixed, 0.10, []string{"OF-1024", "func validate", "```", "感谢大家的支持与配合"}},
	}
}

// TestSemanticSampleSuite 是 6.2 的验收：多语言样例节省率
// 报告 + 关键信息保真断言；任何样例不膨胀。
func TestSemanticSampleSuite(t *testing.T) {
	for _, s := range semanticSamples() {
		t.Run(s.name, func(t *testing.T) {
			got := semanticCompress(s.text, 0.5)
			rate := 1 - float64(len(got))/float64(len(s.text))
			t.Logf("sample %-16s before=%6d after=%6d saved=%.1f%%",
				s.name, len(s.text), len(got), rate*100)
			for _, want := range s.mustKeep {
				if !strings.Contains(got, want) {
					t.Fatalf("key content %q lost", want)
				}
			}
			if rate < s.minOK {
				t.Fatalf("savings rate %.1f%% < %.1f%%", rate*100, s.minOK*100)
			}
			if rate < 0 {
				t.Fatalf("output grew: %d -> %d", len(s.text), len(got))
			}
		})
	}
}

func TestSemanticCompressRules(t *testing.T) {
	// <4 句不动：裁无可裁。
	short := "修复登录页面的空指针。回归测试已补。无其他变更。"
	if got := semanticCompress(short, 0.5); got != short {
		t.Fatalf("short text must stay unchanged: %q", got)
	}
	// 近重复句不参与补选：rate 0.5 下至多 1 份（首现句也可能被裁）；
	// rate 0.9 下其余句全保留，近重复仍收敛到恰 1 份。
	dup := "第一句说明目标 A-100。" +
		strings.Repeat("这个方案我们讨论过很多次了，大家觉得思路可行，没有大的问题。", 1) +
		"中间是其他内容，包含编号 B-200。" +
		"这个方案我们讨论过很多次了，大家觉得思路可行，没有大的问题。" +
		"再一段其他内容，包含编号 C-300。" +
		"这个方案我们讨论过很多次了，大家觉得思路可行，没有大的问题。" +
		"最后一句说明收尾。"
	if n := strings.Count(semanticCompress(dup, 0.5), "这个方案我们讨论过很多次了"); n > 1 {
		t.Fatalf("near-duplicates must not refill, got %d", n)
	}
	if n := strings.Count(semanticCompress(dup, 0.8), "这个方案我们讨论过很多次了"); n != 1 {
		t.Fatalf("near-duplicates must collapse to exactly 1 at high rate, got %d", n)
	}
	// 数字不被腰斩：3.14 / file.go 不产生句边界。
	sents := splitSentences("Pi is 3.14 ok. Then file.go loads. Done.")
	if len(sents) != 3 {
		t.Fatalf("decimals/filenames must not split: %d sentences: %+v", len(sents), sents)
	}
}

// semanticSession 构造含一条可压缩长文本（guard 外）的会话。
func semanticSession(text string) []schema.Message {
	return []schema.Message{
		textMsg(schema.RoleSystem, "You are helpful."),
		textMsg(schema.RoleUser, text),
		textMsg(schema.RoleAssistant, "Understood."),
		textMsg(schema.RoleUser, "Continue."),
	}
}

func TestSemanticStageApplies(t *testing.T) {
	s := semanticSamples()[0]
	in := semanticSession(s.text)
	st := NewSemanticStage(SemanticConfig{
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
	if singleTextOf(out[0]) != "You are helpful." {
		t.Fatal("system must stay untouched")
	}
	if singleTextOf(out[3]) != "Continue." {
		t.Fatal("guarded tail must stay untouched")
	}
	if !strings.Contains(singleTextOf(out[1]), "ACME-7701") {
		t.Fatal("key entity must survive")
	}
}

func TestSemanticRespectsGuardAndThresholds(t *testing.T) {
	s := semanticSamples()[0]
	in := []schema.Message{
		textMsg(schema.RoleUser, s.text),
		textMsg(schema.RoleUser, s.text), // 尾部 guard 内：不动
	}
	st := NewSemanticStage(SemanticConfig{MinTokens: 1, MinTextChars: 100, RecencyGuard: 1})
	out, _, err := st.Apply(in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if singleTextOf(out[1]) != s.text {
		t.Fatal("guarded tail must stay untouched")
	}
	if singleTextOf(out[0]) == s.text {
		t.Fatal("out-of-window text must be rewritten")
	}
	// 简洁会话低于文本阈值：整条不动。
	short := semanticSession("short")
	st2 := NewSemanticStage(SemanticConfig{MinTokens: 1, MinTextChars: 100, RecencyGuard: 2})
	out2, stats2, err := st2.Apply(short)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if singleTextOf(out2[1]) != "short" || stats2.Saved != 0 {
		t.Fatalf("short text must be untouched, stats = %+v", stats2)
	}
}

func TestSemanticTotalsAndShouldRun(t *testing.T) {
	st := NewSemanticStage(SemanticConfig{MinTokens: 5000, RecencyGuard: 2})
	if st.ShouldRun(NewStageContext("m", "s", semanticSession("x"))) {
		t.Fatal("below MinTokens must not run")
	}
	st2 := NewSemanticStage(SemanticConfig{MinTokens: 1, MinTextChars: 100, RecencyGuard: 2})
	for _, s := range semanticSamples() {
		if _, _, err := st2.Apply(semanticSession(s.text)); err != nil {
			t.Fatalf("Apply error: %v", err)
		}
	}
	tot := st2.Totals()
	if tot.Runs != 3 || tot.MessagesRewritten != 3 || tot.Rate() <= 0 || tot.Rate() >= 1 {
		t.Fatalf("totals = %+v", tot)
	}
}

func TestSemanticPipelineIntegration(t *testing.T) {
	s := semanticSamples()[0]
	in := semanticSession(s.text)
	p := NewPipeline(nil,
		NewDedupStage(DedupConfig{MinTokens: 1, RecencyGuard: 2}),
		NewCavemanStage(CavemanConfig{MinTokens: 1, MinTextChars: 100, RecencyGuard: 2}),
		NewSemanticStage(SemanticConfig{MinTokens: 1, MinTextChars: 100, RecencyGuard: 2}))
	out, stats := p.Run(NewStageContext("m", "sess", in), in)
	if len(stats) != 3 {
		t.Fatalf("three stages expected, stats = %+v", stats)
	}
	for i, st := range stats {
		if st.GateRejected != nil || st.Err != nil {
			t.Fatalf("stage %d must pass gate: %+v", i, st)
		}
	}
	if len(singleTextOf(out[1])) >= len(s.text) {
		t.Fatal("expected compressed output")
	}
	if !strings.Contains(singleTextOf(out[1]), "ACME-7701") {
		t.Fatal("key entity must survive the full pipeline")
	}
}

// TestSemanticComboRegistered 验证组合工厂认得两个语义阶段名。
func TestSemanticComboRegistered(t *testing.T) {
	p, err := BuildCombo([]string{"dedup", "caveman", "semantic", "semantic_sidecar"})
	if err != nil {
		t.Fatalf("BuildCombo error: %v", err)
	}
	names := p.StageNames()
	want := []string{"session_dedup", "caveman", "semantic", "semantic_sidecar"}
	for i, w := range want {
		if names[i] != w {
			t.Fatalf("stage %d = %q, want %q", i, names[i], w)
		}
	}
}
