// semantic_text.go 是语义压缩规则档的文本分析原语：多语言
// 分句、词元化（拉丁词 + CJK bigram）、词频信息量打分。全部确定性
// 纯函数，无外部依赖——保真由结构保证（只裁句不造句），重组保持
// 原文顺序与字符完整。
package compression

import (
	"math"
	"strings"
)

// sentence 是分句结果：text 是原文连续片段（重组拼接即还原），
// code 标记 ``` 围栏代码块（整体受保护，不内部断句）。
type sentence struct {
	text string
	code bool
}

// splitSentences 多语言分句：CJK 终结符与换行恒断句；'.' 仅在后随
// 空白/结尾时断（3.14、file.go、e.g. 不断腰）；``` 围栏整体一句。
func splitSentences(text string) []sentence {
	runes := []rune(text)
	var out []sentence
	start, fence := 0, false
	for i := 0; i < len(runes); i++ {
		if atFence(runes, i) {
			if fence {
				out = append(out, sentence{string(runes[start : i+3]), true})
				start, fence = i+3, false
				i += 2
			} else if strings.TrimSpace(string(runes[start:i])) == "" {
				start, fence = i, true
				i += 2
			}
			continue
		}
		if fence || !boundaryAt(runes, i) {
			continue
		}
		if seg := string(runes[start : i+1]); strings.TrimSpace(seg) != "" {
			out = append(out, sentence{seg, false}) // 纯空白段不单列：
			// 并入后句作前导空白（字节保全，重组无缺失）
		}
		start = i + 1
	}
	if start < len(runes) {
		tail := string(runes[start:])
		if strings.TrimSpace(tail) == "" {
			if len(out) > 0 { // 尾随空白并入末句，不丢字节
				out[len(out)-1].text += tail
			}
		} else {
			out = append(out, sentence{tail, fence})
		}
	}
	return out
}

// atFence 判定 i 处是否开始 ``` 三连。
func atFence(runes []rune, i int) bool {
	return runes[i] == '`' && i+2 < len(runes) &&
		runes[i+1] == '`' && runes[i+2] == '`'
}

// boundaryAt 判定 runes[i] 是否句终结（含终结符本身归前句）。
func boundaryAt(runes []rune, i int) bool {
	switch runes[i] {
	case '。', '！', '？', '!', '?', ';', '；', '\n':
		return true
	case '.':
		return i+1 >= len(runes) || isSpaceRune(runes[i+1])
	}
	return false
}

func isSpaceRune(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '　'
}

// tokenize 多语言词元化：拉丁/数字词小写化，CJK 连续字切二元组
// （bigram）——两种文字都有可比较的 token 集。
func tokenize(s string) []string {
	runes := []rune(strings.ToLower(s))
	out := make([]string, 0, len(runes))
	start := -1
	for i, r := range runes {
		switch {
		case isLatinRune(r):
			if start < 0 {
				start = i
			}
		case isCJKRune(r):
			if start >= 0 {
				out = append(out, string(runes[start:i]))
				start = -1
			}
			if i+1 < len(runes) && isCJKRune(runes[i+1]) {
				out = append(out, string(runes[i:i+2]))
			}
		default:
			if start >= 0 {
				out = append(out, string(runes[start:i]))
				start = -1
			}
		}
	}
	if start >= 0 {
		out = append(out, string(runes[start:]))
	}
	return out
}

// isLatinRune 判定构词字符（拉丁字母/数字/下划线/谚文音节块）。
func isLatinRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' ||
		(r >= 0xAC00 && r <= 0xD7A3)
}

// isCJKRune 判定表意文字与假名（bigram 切分域）。
func isCJKRune(r rune) bool {
	return (r >= 0x3040 && r <= 0x30FF) || // 假名
		(r >= 0x3400 && r <= 0x4DBF) || // 扩展 A
		(r >= 0x4E00 && r <= 0x9FFF) || // 基本区
		(r >= 0xF900 && r <= 0xFAFF) // 兼容表意
}

// semanticScores 为每句计算信息量分：词频倒数（稀有=高信息）求和
// 按句长平方根归一（长短句公平），含数字/疑问/代码标记加权。
func semanticScores(sents []sentence) []float64 {
	freq := map[string]int{}
	toks := make([][]string, len(sents))
	for i, s := range sents {
		toks[i] = tokenize(s.text)
		for _, t := range toks[i] {
			freq[t]++
		}
	}
	scores := make([]float64, len(sents))
	for i, s := range sents {
		var sum float64
		for _, t := range toks[i] {
			sum += 1 / float64(freq[t])
		}
		if n := len(toks[i]); n > 0 {
			sum /= math.Sqrt(float64(n))
		}
		scores[i] = sum * infoBoost(s.text)
	}
	return scores
}

// infoBoost 返回句子的信息加权系数（数字、疑问、代码标记）。
func infoBoost(text string) float64 {
	f := 1.0
	if strings.ContainsAny(text, "0123456789") {
		f *= 1.5
	}
	if t := strings.TrimRight(text, " \t\n\r"); strings.HasSuffix(t, "?") || strings.HasSuffix(t, "？") {
		f *= 1.5
	}
	if codeMarked(text) {
		f *= 2
	}
	return f
}

// codeMarked 判定句内是否含代码标记（代码句信息密度高，加权保留）。
func codeMarked(text string) bool {
	for _, m := range []string{"```", "func ", "def ", "const ", "=>", "==", ":=", "<-", "http://", "https://"} {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// jaccard 是 token 集相似度（近重复句判定）。
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for t := range a {
		if b[t] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}
