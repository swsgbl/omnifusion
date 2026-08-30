// mlrouter_test.go 覆盖 启发式特征升降档、阈值决策、分类器
// 可互换（ONNX 未来同接口）、Totals 计数，以及验收要求的
// 「对比规则路由的 A/B 报告」（规则路由恒定选强档 vs ML 按难度分流
// 的成本差）。
package intelligence

import (
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

func textReq(text string) *schema.UnifiedRequest {
	return &schema.UnifiedRequest{
		Model:    "@smart",
		Messages: []schema.Message{{Role: "user", Content: schema.NewTextContent(text)}},
	}
}

func TestHeuristicDifficultyBounds(t *testing.T) {
	var hc HeuristicClassifier
	if d := hc.Difficulty(nil); d != 0 {
		t.Errorf("nil 请求难度 = %v, want 0", d)
	}
	if d := hc.Difficulty(textReq("hi there!")); d != 0 {
		t.Errorf("寒暄难度 = %v, want 0（寒暄 -0.2 钳到 0）", d)
	}
	// 全特征叠加：代码 + 深动词 + 工具 + 多模态 + 历史 + 长文 = 钳 1。
	hard := &schema.UnifiedRequest{
		Model: "@smart",
		Messages: []schema.Message{
			{Role: "user", Content: schema.Content{Parts: []schema.Part{
				{Type: schema.PartText, Text: strings.Repeat("debug this func ", 900)},
				{Type: schema.PartImageURL},
			}}},
		},
		Tools: []schema.Tool{{Type: "function"}},
	}
	for i := 0; i < 12; i++ { // 历史条数 >10
		hard.Messages = append(hard.Messages, schema.Message{Role: "assistant", Content: schema.NewTextContent("ok")})
	}
	if d := hc.Difficulty(hard); d != 1 {
		t.Errorf("全特征难度 = %v, want 钳位 1", d)
	}
}

func TestHeuristicFeatureDeltas(t *testing.T) {
	var hc HeuristicClassifier
	base := hc.Difficulty(textReq("please help me with this thing"))
	if base != 0 {
		t.Fatalf("基线难度 = %v, want 0", base)
	}
	check := func(name string, req *schema.UnifiedRequest, want float64) {
		t.Helper()
		if d := hc.Difficulty(req); d != want {
			t.Errorf("%s 难度 = %v, want %v", name, d, want)
		}
	}
	check("代码标记", textReq("look at this:\nfunc main() {}"), 0.2)
	check("深动词", textReq("please analyze the dataset"), 0.15)
	check("浅任务", textReq("please translate this sentence"), 0) // -0.2 钳 0
	check("长文本", textReq(strings.Repeat("word ", 800)), 0.15)  // 4000 字符 ≈ 1000 token

	tools := textReq("x")
	tools.Tools = []schema.Tool{{Type: "function"}}
	check("工具定义", tools, 0.15)

	calls := textReq("x")
	calls.Messages = append(calls.Messages, schema.Message{Role: "assistant", ToolCalls: []schema.ToolCall{{ID: "1", Type: "function"}}})
	check("tool_calls", calls, 0.1)

	mm := textReq("x")
	mm.Messages[0].Content = schema.Content{Parts: []schema.Part{
		{Type: schema.PartText, Text: "x"}, {Type: schema.PartImageURL},
	}}
	check("多模态", mm, 0.25)

	js := textReq("extract fields")
	js.ResponseFormat = []byte(`{"type":"json_schema"}`)
	check("json_schema", js, 0.1)
}

func TestRouteTierDecisions(t *testing.T) {
	ml := NewMLRouter(
		MLTarget{Provider: "a", Model: "m-weak"},
		MLTarget{Provider: "b", Model: "m-strong"},
	)
	if got := ml.EffectiveThreshold(); got != DefaultMLThreshold {
		t.Errorf("默认阈值 = %v, want %v", got, DefaultMLThreshold)
	}
	easy := ml.Route(textReq("hi there!"))
	if easy.Tier != TierWeak || easy.Confidence != 1 {
		t.Errorf("easy = %+v, want weak/置信 1", easy)
	}
	if easy.Members[0].Model != "m-weak" || easy.Members[1].Model != "m-strong" {
		t.Errorf("easy 成员序 = %v, want 弱档在前", easy.Members)
	}
	hard := &schema.UnifiedRequest{
		Model: "@smart",
		Messages: []schema.Message{{Role: "user", Content: schema.Content{Parts: []schema.Part{
			{Type: schema.PartText, Text: "debug:\nfunc main() {}" + strings.Repeat(" pad ", 500)},
			{Type: schema.PartImageURL},
		}}}},
		Tools: []schema.Tool{{Type: "function"}},
	}
	d := ml.Route(hard) // 0.25+0.2+0.15+0.15 = 0.75 ≥ 0.55
	if d.Tier != TierStrong {
		t.Errorf("hard 档位 = %s（难度 %v）, want strong", d.Tier, d.Difficulty)
	}
	if d.Members[0].Model != "m-strong" || d.Members[1].Model != "m-weak" {
		t.Errorf("hard 成员序 = %v, want 强档在前", d.Members)
	}

	ml.Threshold = 0.9 // 自定义阈值：0.75 落回弱档
	if d := ml.Route(hard); d.Tier != TierWeak {
		t.Errorf("阈值 0.9 下档位 = %s, want weak", d.Tier)
	}
}

// fixedClassifier 是可互换性证明：任意实现（未来 ONNX）
// 注入即接管分档。
type fixedClassifier struct{ v float64 }

func (f fixedClassifier) Difficulty(*schema.UnifiedRequest) float64 { return f.v }

func TestClassifierSwappable(t *testing.T) {
	ml := NewMLRouter(MLTarget{Provider: "a", Model: "w"}, MLTarget{Provider: "b", Model: "s"})
	ml.Classifier = fixedClassifier{v: 0.99}
	if d := ml.Route(textReq("hi")); d.Tier != TierStrong {
		t.Errorf("注入分类器 0.99 → 档位 %s, want strong", d.Tier)
	}
	ml.Classifier = fixedClassifier{v: 0.01}
	if d := ml.Route(textReq("solve everything")); d.Tier != TierWeak {
		t.Errorf("注入分类器 0.01 → 档位 %s, want weak", d.Tier)
	}
}

func TestTotalsCounting(t *testing.T) {
	ml := NewMLRouter(MLTarget{Provider: "a", Model: "w"}, MLTarget{Provider: "b", Model: "s"})
	ml.Route(textReq("hi"))
	ml.Route(textReq("hello"))
	ml.Route(&schema.UnifiedRequest{ // 强档：多模态+代码+工具
		Messages: []schema.Message{{Role: "user", Content: schema.Content{Parts: []schema.Part{
			{Type: schema.PartText, Text: "func (){}"}, {Type: schema.PartImageURL},
		}}}},
		Tools: []schema.Tool{{Type: "function"}},
	})
	if tot := ml.Totals(); tot != (MLTotals{Decisions: 3, Weak: 2, Strong: 1}) {
		t.Errorf("Totals = %+v, want 3/2/1", tot)
	}
}

// abCase 是 A/B 报告的一个样例请求。
type abCase struct {
	name string
	req  *schema.UnifiedRequest
}

// TestMLABReportVsRuleRouting 是 验收面：规则路由（恒定选首家
// =强档计价）vs ML 路由（按难度分流）。成本单位 weak=1 / strong=10。
func TestMLABReportVsRuleRouting(t *testing.T) {
	cases := []abCase{
		{"寒暄", textReq("hi there!")},
		{"翻译", textReq("please translate this note to English")},
		{"摘要", textReq("summarize: " + strings.Repeat("milk eggs bread ", 20))},
		{"代码调试", &schema.UnifiedRequest{
			Messages: []schema.Message{{Role: "user", Content: schema.NewTextContent(
				"debug why this fails:\nfunc main() { panic(1) }\n" + strings.Repeat("context ", 800))}},
			Tools: []schema.Tool{{Type: "function"}},
		}},
		{"视觉分析", &schema.UnifiedRequest{
			Messages: []schema.Message{{Role: "user", Content: schema.Content{Parts: []schema.Part{
				{Type: schema.PartText, Text: "analyze the chart"}, {Type: schema.PartImageURL},
			}}}},
			Tools: []schema.Tool{{Type: "function"}},
		}},
		{"长文重构", &schema.UnifiedRequest{
			Messages: []schema.Message{{Role: "user", Content: schema.NewTextContent(
				"refactor this module:\n```go\nfunc a(){}\n```\n" + strings.Repeat("context ", 1200))}},
			Tools:          []schema.Tool{{Type: "function"}},
			ResponseFormat: []byte(`{"type":"json_schema"}`),
		}},
	}
	ml := NewMLRouter(MLTarget{Provider: "a", Model: "m-weak"}, MLTarget{Provider: "b", Model: "m-strong"})

	const weakCost, strongCost = 1, 10
	ruleCost, mlCost := 0, 0
	t.Logf("A/B 报告：规则路由（恒定强档）vs ML 路由（启发式分档，阈值 %.2f）", ml.EffectiveThreshold())
	t.Logf("%-8s %9s %6s %6s", "case", "difficulty", "rule", "ml")
	for _, c := range cases {
		d := ml.Route(c.req)
		ruleCost += strongCost
		mlCost += weakCost
		if d.Tier == TierStrong {
			mlCost += strongCost - weakCost
		}
		t.Logf("%-8s %9.2f %6s %6s", c.name, d.Difficulty, "strong", d.Tier)
	}
	tot := ml.Totals()
	t.Logf("合计：规则 %d 单位；ML %d 单位（weak=%d strong=%d）；节省 %.1f%%",
		ruleCost, mlCost, tot.Weak, tot.Strong,
		100*float64(ruleCost-mlCost)/float64(ruleCost))
	if tot.Weak == 0 || tot.Strong == 0 {
		t.Fatalf("分流异常（weak=%d strong=%d）：样例集应两档皆有", tot.Weak, tot.Strong)
	}
	if mlCost >= ruleCost {
		t.Fatalf("ML 成本 %d 未低于规则 %d", mlCost, ruleCost)
	}
}
