// redact.go 是密钥形态脱敏器（蓝图 Phase 8：secret 不得进入
// logs/traces/prompts/errors——攻击者视角审计的对策件）。上游错误
// 体（UpstreamError 保留 512B）可能回显 Authorization 或密钥本体：
// 所有把上游原文带出的面（日志/错误串）必须先过 RedactSecrets。
// security 是依赖叶子（仅标准库），可被任意层复用。
package security

import "regexp"

// redactRules 是 (模式, 替换) 对：保守表——只打标准密钥形态与
// Bearer/键值上下文，普通文本（模型名/URL/单词）零误伤。
var redactRules = []struct {
	re   *regexp.Regexp
	repl string
}{
	// Authorization 载荷：Bearer/Basic + token（保留方案名）。
	{regexp.MustCompile(`(?i)\b(bearer|basic)(\s+)[A-Za-z0-9._~+/=-]{8,}`), "${1}${2}[REDACTED]"},
	// 键值形态：api_key=… / "token": …（保留键名与分隔）。
	{regexp.MustCompile(`(?i)\b(api[_-]?key|apikey|secret|token|password)(["' ]*[:=][ ]*["']?)[A-Za-z0-9._~+/=-]{8,}`), "${1}${2}[REDACTED]"},
	// OpenAI / OpenRouter。
	{regexp.MustCompile(`\bsk-(?:proj-|or-)?[A-Za-z0-9_-]{8,}\b`), "[REDACTED]"},
	// GitHub PAT。
	{regexp.MustCompile(`\bgh[a-z]_[A-Za-z0-9]{8,}\b`), "[REDACTED]"},
	// Slack。
	{regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{8,}\b`), "[REDACTED]"},
	// 本网关 token（master ofg-/scoped ofm-）。
	{regexp.MustCompile(`\bof[gm]-[A-Za-z0-9]{8,}\b`), "[REDACTED]"},
	// 火山方舟。
	{regexp.MustCompile(`\bark-[A-Za-z0-9-]{8,}\b`), "[REDACTED]"},
	// Google AI。
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{10,}\b`), "[REDACTED]"},
	// HuggingFace / GitLab。
	{regexp.MustCompile(`\bhf_[A-Za-z0-9]{8,}\b`), "[REDACTED]"},
	{regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{8,}\b`), "[REDACTED]"},
	// JWT 三段形态。
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`), "[REDACTED]"},
	// 长十六进制（≥40：SHA/随机密钥形态；文档正文极少出现）。
	{regexp.MustCompile(`\b[0-9a-fA-F]{40,}\b`), "[REDACTED]"},
}

// RedactSecrets 把字符串中的密钥形态片段替换为 [REDACTED]（保留
// Bearer/键名上下文）。幂等：已脱敏串再过一遍不变。
func RedactSecrets(s string) string {
	for _, r := range redactRules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}
