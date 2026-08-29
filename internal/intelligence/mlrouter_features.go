// mlrouter_features.go 承载 M6.3 难度分类器：DifficultyClassifier
// 接口（ADR-009：默认纯 Go 启发式，未来 ONNX 实现同接口互换）与
// HeuristicClassifier 的特征打分。特征学 RouteLLM 的可分性信号：
// 长度、代码、工具调用、多模态、任务动词——每项 ±0.1..0.3 求和后
// 钳 [0,1]；不追求精确回归，只要弱/强两簇可分。
package intelligence

import (
	"strings"

	"github.com/swsgbl/omnifusion/internal/compression"
	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// DifficultyClassifier 给请求打难度分（0..1，1=需强模型）。
// 实现必须是无锁纯函数（Route 内联调用，流式/非流式高频路径）。
type DifficultyClassifier interface {
	Difficulty(req *schema.UnifiedRequest) float64
}

// HeuristicClassifier 是默认纯 Go 启发式分类器（零依赖零状态）。
type HeuristicClassifier struct{}

// Difficulty 按特征加权求和：长上下文 +0.3/0.15、代码标记 +0.2、
// 工具定义 +0.15、历史含 tool_calls +0.1、多模态 +0.25、消息 >10 条
// +0.1、深任务动词 +0.15、json schema 输出 +0.1；翻译/摘要类 -0.2、
// 短寒暄 -0.2。求和钳 [0,1]。
func (HeuristicClassifier) Difficulty(req *schema.UnifiedRequest) float64 {
	if req == nil {
		return 0
	}
	text := requestText(req)
	d := lengthDifficulty(compression.EstimateTokens(req.Messages))
	if containsAny(text, codeMarkers) {
		d += 0.2
	}
	if len(req.Tools) > 0 {
		d += 0.15
	}
	if hasToolCalls(req) {
		d += 0.1
	}
	if hasMultimodal(req) {
		d += 0.25
	}
	if len(req.Messages) > 10 {
		d += 0.1
	}
	if matchesTaskWord(text, deepTaskWords) {
		d += 0.15
	}
	if isStructuredOutput(req) {
		d += 0.1
	}
	if matchesTaskWord(text, lightTaskWords) {
		d -= 0.2
	} else if len(text) < 200 && matchesTaskWord(text, chitchatWords) {
		d -= 0.2 // 寒暄信号只在小请求上可信（长文里的 "thanks" 不降档）
	}
	return clampUnit(d)
}

// lengthDifficulty：长上下文本身即难度信号（检索/综合跨度大）。
// token 估算复用 L4 压缩的同源口径（intelligence→compression 单向
// 依赖，无环）。
func lengthDifficulty(tokens int) float64 {
	switch {
	case tokens > 2000:
		return 0.3
	case tokens > 800:
		return 0.15
	default:
		return 0
	}
}

func hasToolCalls(req *schema.UnifiedRequest) bool {
	for i := range req.Messages {
		if len(req.Messages[i].ToolCalls) > 0 {
			return true
		}
	}
	return false
}

// hasMultimodal：任一非文本部分（image_url/input_audio/file）即
// 视觉/音频理解任务（弱模型普遍不支持或质量差）。
func hasMultimodal(req *schema.UnifiedRequest) bool {
	for i := range req.Messages {
		for _, p := range req.Messages[i].Content.Parts {
			if p.Type != schema.PartText {
				return true
			}
		}
	}
	return false
}

// isStructuredOutput：json_schema/json_object 输出约束需要精确指令
// 遵循（弱模型易违反 schema）。
func isStructuredOutput(req *schema.UnifiedRequest) bool {
	if len(req.ResponseFormat) == 0 {
		return false
	}
	s := string(req.ResponseFormat)
	return strings.Contains(s, "json_schema") || strings.Contains(s, "json_object")
}

// requestText 拼接全部消息文本（特征扫描面；忽略非文本部分）。
func requestText(req *schema.UnifiedRequest) string {
	var b strings.Builder
	for i := range req.Messages {
		b.WriteString(req.Messages[i].Content.TextOf())
		b.WriteByte('\n')
	}
	return b.String()
}

// codeMarkers 是代码信号子串（正文命中其一即视为含代码）。
var codeMarkers = []string{"```", "func ", "def ", "class ", "const ", "=>", ":=", "<-", "import "}

// taskWords 是一组任务动词信号：EN 按词元、CJK 按子串匹配。
type taskWords struct {
	words map[string]bool
	cjk   []string
}

// deepTaskWords 是深任务信号（推理/工程动词）。
var deepTaskWords = taskWords{
	words: map[string]bool{
		"analyze": true, "debug": true, "refactor": true, "optimize": true,
		"implement": true, "architect": true, "diagnose": true, "derive": true, "prove": true,
	},
	cjk: []string{"分析", "调试", "重构", "排查", "优化", "推导", "证明", "逐步"},
}

// lightTaskWords 是浅任务信号（翻译/摘要/改写：弱模型可胜任）。
var lightTaskWords = taskWords{
	words: map[string]bool{
		"translate": true, "summarize": true, "summarise": true, "paraphrase": true,
	},
	cjk: []string{"翻译", "总结", "摘要", "改写", "润色"},
}

// chitchatWords 是寒暄信号（仅短文本上生效，见 Difficulty）。
var chitchatWords = taskWords{
	words: map[string]bool{"hello": true, "hi": true, "hey": true, "thanks": true, "thank": true, "ok": true},
	cjk:   []string{"你好", "谢谢", "嗨", "哈喽"},
}

// matchesTaskWord：EN 词元小写化匹配（避免 "hi" 命中 "graphi "），
// 词元剥离首尾标点（"thanks!" ≠ "thanks" 会漏配）；CJK 无词界直接
// 子串匹配。
func matchesTaskWord(text string, tw taskWords) bool {
	lower := strings.ToLower(text)
	for _, s := range tw.cjk {
		if strings.Contains(lower, s) {
			return true
		}
	}
	for _, w := range strings.Fields(lower) {
		w = strings.Trim(w, "!.,?;:~\"'`()[]{}<>|/\\-_=+*#%$&^@：，。！？；「」『』（）《》…—～")
		if tw.words[w] {
			return true
		}
	}
	return false
}

func containsAny(text string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

func clampUnit(d float64) float64 {
	if d < 0 {
		return 0
	}
	if d > 1 {
		return 1
	}
	return d
}
