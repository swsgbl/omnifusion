// guardrails.go 实现规则型 Guardrails v1（ L2）：入站正文
// 的 PII 检测（默认拦截）与提示注入模式告警（默认放行+告警）。纯规则、
// 无外部依赖；Finding 只含规则名与计数、不含命中原文——日志不二次泄漏。
package security

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Action 是一类检测命中的处置方式。
type Action string

// 处置枚举。PII 默认 block（拦截），注入默认 warn（告警放行）。
const (
	ActionOff   Action = "off"
	ActionWarn  Action = "warn"
	ActionBlock Action = "block"
)

// Finding 是一次扫描的命中汇总（去重计数，无原文）。
type Finding struct {
	Kind  string `json:"kind"` // pii | injection
	Rule  string `json:"rule"`
	Count int    `json:"count"`
}

// GuardrailsOptions 控制规则集与处置；零值字段取默认。
type GuardrailsOptions struct {
	PIIAction       Action   // 默认 block
	InjectionAction Action   // 默认 warn
	PIITypes        []string // 选用 PII 规则名；nil=全部
}

// rule 是一条检测规则；verify 是可选后验（校验和类规则防误报）。
type rule struct {
	name   string
	kind   string
	re     *regexp.Regexp
	verify func(string) bool
}

// piiRules 是内置 PII 规则（顺序即文档顺序）。
var piiRules = []rule{
	{name: "email", kind: "pii", re: regexp.MustCompile(
		`(?i)\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)},
	{name: "phone_cn", kind: "pii", re: regexp.MustCompile(
		`\b1[3-9]\d{9}\b`)},
	{name: "cn_id", kind: "pii", re: regexp.MustCompile(
		`\b\d{17}[\dXx]\b`), verify: validCNID},
	{name: "bank_card", kind: "pii", re: regexp.MustCompile(
		`\b\d{13,19}\b`), verify: luhnOK},
	{name: "secret_key", kind: "pii", re: regexp.MustCompile(
		`(?i)\b(?:sk|rk|pk|ghp|gho|xoxb|glpat)[-_][A-Za-z0-9]{16,}\b`)},
}

// injectionRules 是内置提示注入模式（EN+CN，告警用）。
var injectionRules = []rule{
	{name: "override_en", kind: "injection", re: regexp.MustCompile(
		`(?i)ignore\s+(?:all\s+|any\s+|the\s+)?(?:previous|prior|above|earlier)\s+(?:instructions?|prompts?|rules?|directions?)`)},
	{name: "disregard_en", kind: "injection", re: regexp.MustCompile(
		`(?i)disregard\s+(?:all\s+|any\s+|the\s+|your\s+)?(?:previous|prior|above|earlier\s+|instructions?|prompts?|directions?)`)},
	{name: "exfil_prompt_en", kind: "injection", re: regexp.MustCompile(
		`(?i)(?:reveal|show|print|repeat|output|leak)\s+(?:your\s+|the\s+|any\s+)?(?:system\s+)?(?:prompt|instructions?|rules)`)},
	{name: "persona_en", kind: "injection", re: regexp.MustCompile(
		`(?i)(?:you\s+are\s+now|act\s+as\s+if\s+you\s+(?:are|were)|act\s+as\s+a\s+different)`)},
	{name: "jailbreak_en", kind: "injection", re: regexp.MustCompile(
		`(?i)\bjailbreak\b|\bdan\s+mode\b`)},
	{name: "override_cn", kind: "injection", re: regexp.MustCompile(
		`忽略(?:掉)?(?:之前|以上|前面|上述|所有)(?:的)?(?:指令|提示|设定|规则|内容)`)},
	{name: "disregard_cn", kind: "injection", re: regexp.MustCompile(
		`无视(?:掉)?(?:之前|以上|所有)(?:的)?(?:指令|提示|设定|规则)`)},
	{name: "exfil_prompt_cn", kind: "injection", re: regexp.MustCompile(
		`(?:透露|输出|复述|泄露)(?:你的)?(?:系统提示|系统指令|初始指令)`)},
	{name: "persona_cn", kind: "injection", re: regexp.MustCompile(
		`从现在开始你是|请你扮演|你现在是`)},
}

// Guardrails 是装配后的检测器；零值不可用，经 NewGuardrails 构造。
type Guardrails struct {
	pii       []rule
	injection []rule
	piiAct    Action
	injAct    Action
}

// DefaultPIIRuleNames 返回内置 PII 规则名（配置白名单提示用）。
func DefaultPIIRuleNames() []string {
	names := make([]string, len(piiRules))
	for i, r := range piiRules {
		names[i] = r.name
	}
	return names
}

// NewGuardrails 按选项构造；未知处置值或未知 PII 规则名报错（装配期
// fail-fast，语义错误不静默吞）。
func NewGuardrails(opts GuardrailsOptions) (*Guardrails, error) {
	g := &Guardrails{piiAct: opts.PIIAction, injAct: opts.InjectionAction}
	if g.piiAct == "" {
		g.piiAct = ActionBlock
	}
	if g.injAct == "" {
		g.injAct = ActionWarn
	}
	for _, a := range []Action{g.piiAct, g.injAct} {
		switch a {
		case ActionOff, ActionWarn, ActionBlock:
		default:
			return nil, fmt.Errorf("guardrails: invalid action %q (off|warn|block)", a)
		}
	}
	known := map[string]bool{}
	for _, r := range piiRules {
		known[r.name] = true
	}
	if opts.PIITypes == nil {
		g.pii = piiRules
	} else {
		for _, name := range opts.PIITypes {
			if !known[name] {
				return nil, fmt.Errorf("guardrails: unknown pii type %q (known: %s)",
					name, strings.Join(DefaultPIIRuleNames(), ", "))
			}
			for _, r := range piiRules {
				if r.name == name {
					g.pii = append(g.pii, r)
				}
			}
		}
	}
	g.injection = injectionRules
	return g, nil
}

// Report 是一次请求正文的检测结论。
type Report struct {
	Blocked  bool      `json:"blocked"`
	Findings []Finding `json:"findings"`
}

// PIIAction 返回 PII 处置（装配日志/测试用）。
func (g *Guardrails) PIIAction() Action { return g.piiAct }

// InjectionAction 返回注入处置（装配日志/测试用）。
func (g *Guardrails) InjectionAction() Action { return g.injAct }

// Inspect 扫多段文本（消息正文集合），按规则去重计数；Blocked 由处置
// 动作决定（block 命中即拦截，warn 只记录，off 整类不扫）。
func (g *Guardrails) Inspect(texts []string) Report {
	counts := map[string]int{}
	for _, text := range texts {
		groups := [][]rule{}
		if g.piiAct != ActionOff {
			groups = append(groups, g.pii)
		}
		if g.injAct != ActionOff {
			groups = append(groups, g.injection)
		}
		for _, rs := range groups {
			for _, r := range rs {
				for _, m := range r.re.FindAllString(text, -1) {
					if r.verify != nil && !r.verify(m) {
						continue
					}
					counts[r.kind+"/"+r.name]++
				}
			}
		}
	}
	if len(counts) == 0 {
		return Report{}
	}
	rep := Report{Findings: make([]Finding, 0, len(counts))}
	for key, n := range counts {
		kind, name, _ := strings.Cut(key, "/")
		rep.Findings = append(rep.Findings, Finding{Kind: kind, Rule: name, Count: n})
		if kind == "pii" && g.piiAct == ActionBlock {
			rep.Blocked = true
		}
		if kind == "injection" && g.injAct == ActionBlock {
			rep.Blocked = true
		}
	}
	sort.Slice(rep.Findings, func(i, j int) bool {
		if rep.Findings[i].Kind != rep.Findings[j].Kind {
			return rep.Findings[i].Kind < rep.Findings[j].Kind
		}
		return rep.Findings[i].Rule < rep.Findings[j].Rule
	})
	return rep
}

// Summary 拼命中摘要（规则名×计数，无原文），用于错误消息与日志。
func (rep Report) Summary() string {
	parts := make([]string, len(rep.Findings))
	for i, f := range rep.Findings {
		parts[i] = fmt.Sprintf("%s/%s ×%d", f.Kind, f.Rule, f.Count)
	}
	return strings.Join(parts, ", ")
}

// validCNID 校验 GB 11643-1999 身份证校验位（17 位本体 + 加权模 11 映射）。
func validCNID(s string) bool {
	weights := [17]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	checks := "10X98765432"
	sum := 0
	for i := 0; i < 17; i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
		sum += int(s[i]-'0') * weights[i]
	}
	last := s[17] | 0x20 // X/x 归一
	want := checks[sum%11]
	return last == (want | 0x20)
}

// luhnOK 校验 Luhn（银行卡/信用卡校验位）。
func luhnOK(s string) bool {
	sum, alt := 0, false
	for i := len(s) - 1; i >= 0; i-- {
		d := int(s[i] - '0')
		if d < 0 || d > 9 {
			return false
		}
		if alt {
			if d *= 2; d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
	}
	return sum%10 == 0
}
