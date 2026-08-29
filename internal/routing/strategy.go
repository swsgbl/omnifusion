// strategy.go 是 M2.5 策略框架（docs/04 §4.2）：Strategy 对候选列表做
// 稳定排序，产出尝试顺序。与弹性层分工：隔离/配额阻断是硬边界（先短路），
// 策略只对幸存者排序；打分路由（M2.4）即默认的 auto 策略。
//
// 选择入口（server 层解析，见 HeaderStrategy 与 ParseModelDirective）：
//   - model 内嵌指令："@fast:llama-3.3-70b"（in-band，优先）
//   - 请求 header：X-OmniFusion-Strategy: fast（out-of-band）
//
// "@combo:…"（命名模型组）属于后续 combo 存储，共用同一 @ 命名空间。
// quality 策略（2026-08-29 补齐）按社区 feed 能力分降序——「免费模型
// 路由器」的能力排序半边。
package routing

import (
	"fmt"
	"sort"
	"strings"

	"github.com/swsgbl/omnifusion/internal/provider"
)

// HeaderStrategy 是经 header 选择策略的请求头名。
const HeaderStrategy = "X-OmniFusion-Strategy"

// RouteContext 携带跨层路由输入（docs/04 §4.2）：v1 只有策略名；
// M4.5 起接入压缩后 token 数（PromptTokens），M6 复杂度分数随后，
// quality 策略（2026-08-29）起携带目标模型名（能力分查询键）。
type RouteContext struct {
	StrategyName string
	// PromptTokens 是 L4 压缩管线输出的请求粗估 token（跨层协同：
	// 压缩后 token 参与候选筛选/打分，docs/04 §4.3）。0=未知，不过滤。
	PromptTokens int64
	// Model 是本次请求的目标模型（裸名，指令解析后）——quality 策略
	// 以 (provider, Model) 查社区能力分排序；空 = 无目标模型语义。
	Model string
}

// Strategy 决定候选 provider 的尝试顺序（稳定排序，不改成员）。
type Strategy interface {
	Name() string
	Order(cands []provider.Provider, rc *RouteContext) []provider.Provider
}

// ParseModelDirective 解析 model 字段的 @ 指令前缀：
//
//	"@fast:llama-3.3-70b" → ("fast", "llama-3.3-70b", nil)
//	"@cheap"              → ("cheap", "", nil)  // 无目标模型，由调用方裁决
//	"llama-3.3-70b"       → ("", "llama-3.3-70b", nil)
//	"@combo:free-tier"    → ("combo", "free-tier", nil)  // M4.7：rest 是组合名
//	"@bogus:x"            → 错误（未知指令名）
func ParseModelDirective(model string) (strategy, bare string, err error) {
	if !strings.HasPrefix(model, "@") {
		return "", model, nil
	}
	name, rest, _ := strings.Cut(strings.TrimPrefix(model, "@"), ":")
	if !knownDirectiveName(name) {
		return "", "", fmt.Errorf("unknown strategy %q (known: auto, %s, combo:<name>)", name, strings.Join(builtinStrategyNames, ", "))
	}
	return name, rest, nil
}

// DirectiveCombo 是组合指令名（共用 @ 命名空间，但不是策略：
// server 边界转为 WithCombo，不进策略引擎）。
const DirectiveCombo = "combo"

// DirectiveFusion 是 Fusion 指令名（M6.1）：model 内嵌 "@fusion" 触发
// 并行扇出 + Judge 合成（server 边界短路到 FusionRunner，不进候选
// 选择）。无目标模型：合成成员来自配置 fusion 段。
const DirectiveFusion = "fusion"

// DirectiveSmart 是 ML 路由指令名（M6.3）：model 内嵌 "@smart" 触发
// 逐请求难度分类的弱/强分档（server 边界转为 WithSmart，候选=Router.Smart
// 计划成员）。无目标模型：档位成员来自配置 mlrouter 段。
const DirectiveSmart = "smart"

// DirectiveQuality 是能力排序指令名（2026-08-29）："@quality:model" 按
// 能力分排候选；裸 "@quality"（无目标模型）自动选各家最强模型逐尝试
//（server 边界转为 WithQualityAuto）。
const DirectiveQuality = "quality"

var builtinStrategyNames = []string{"priority", "cheap", "fast", "lkgp", "quality"}

func knownDirectiveName(name string) bool {
	if name == "auto" || name == DirectiveCombo || name == DirectiveFusion || name == DirectiveSmart {
		return true
	}
	for _, n := range builtinStrategyNames {
		if n == name {
			return true
		}
	}
	return false
}

// DispatchOption 是 Dispatch/DispatchStream 的逐请求选项。
type DispatchOption func(*dispatchConfig)

type dispatchConfig struct {
	strategyName   string
	sessionID      string // sticky session（M2.7）
	promptTokens   int64  // 压缩后 token（M4.5 跨层输入；0=未知）
	comboName      string // 命名模型组（M4.7；空=普通分发）
	pinnedProvider string // 全局路由钉选（M5.2；空=未钉）
	targetProvider string // 定向分发 provider（M6.1 Fusion；空=普通路径）
	targetModel    string // 定向分发模型（与 targetProvider 成对）
	smart          bool   // ML 路由（M6.3 "@smart"；候选=Smart 计划成员）
	qualityAuto    bool   // 裸 "@quality"（自动选最强：按 BestModel 排序并逐家改写模型）
}

// WithStrategyName 覆盖本次分发的策略（默认 auto = M2.4 打分排序）。
func WithStrategyName(name string) DispatchOption {
	return func(c *dispatchConfig) { c.strategyName = name }
}

// WithCombo 选择命名模型组（M4.7）：候选=组合成员（声明序），逐尝试
// 把 req.Model 改写为成员模型；绑定的压缩组合由 server 边界应用。
func WithCombo(name string) DispatchOption {
	return func(c *dispatchConfig) { c.comboName = name }
}

// WithPromptTokens 携带压缩后 token 估算（M4.5 跨层：L4 压缩管线
// 的产出喂给 L3，让小上下文模型因压缩进入候选）。
func WithPromptTokens(n int64) DispatchOption {
	return func(c *dispatchConfig) { c.promptTokens = n }
}

// WithPinnedProvider 钉选全局路由目标（M5.2 路由切换）：钉选 provider
// 仍在候选时提到尝试序列首位（其余候选保留做 failover——隔离/配额
// 硬边界照常短路）；不在候选则忽略。组合路径（@combo）不受影响。
func WithPinnedProvider(name string) DispatchOption {
	return func(c *dispatchConfig) { c.pinnedProvider = name }
}

// WithTarget 定向分发（M6.1 Fusion 扇出/合成原语）：跳过候选选择与
// 策略排序，直接把请求发往声明的 (provider, model)（成员模型重写、
// 隔离/配额/打分观测与普通路径完全一致）。provider 未装配 → 本次
// 分发失败（不静默换家：Fusion 成员是配置真相源）。
func WithTarget(provider, model string) DispatchOption {
	return func(c *dispatchConfig) { c.targetProvider, c.targetModel = provider, model }
}

func resolveOptions(opts []DispatchOption) dispatchConfig {
	var cfg dispatchConfig
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// strategyByName 解析策略名；auto/空 → nil（走 M2.4 rank）。
func (r *Router) strategyByName(name string) Strategy {
	switch name {
	case "", "auto":
		return nil // rank() 的加权打分即 auto 策略
	case "priority":
		return priorityStrategy{}
	case "cheap":
		return cheapStrategy{q: r.Quota}
	case "fast":
		return fastStrategy{s: r.Scoring}
	case "lkgp":
		return lkgpStrategy{s: r.Scoring}
	case "quality":
		return qualityStrategy{cap: r.Capability}
	default:
		return nil // 未知名在边界层已拒绝；兜底回注册序
	}
}

// ordered 返回本次分发的候选顺序：策略排序（M2.5）> 打分排序（M2.4）
// > 注册序（M1）。Scoring 未启用时 rank() 退化为注册序。tokens 是
// 压缩后 token 估算、model 是目标模型（裸名）——都喂给策略的
// RouteContext（quality 以 model 查能力分）。
func (r *Router) ordered(name, model string, tokens int64) []provider.Provider {
	st := r.strategyByName(name)
	if st == nil {
		return r.rank()
	}
	return st.Order(r.Providers, &RouteContext{
		StrategyName: name,
		PromptTokens: tokens,
		Model:        model,
	})
}

// priorityStrategy 保持配置序（用户声明的优先级即真相）。
type priorityStrategy struct{}

func (priorityStrategy) Name() string { return "priority" }

func (priorityStrategy) Order(cands []provider.Provider, _ *RouteContext) []provider.Provider {
	return cands
}

// cheapStrategy 按剩余配额余量降序：先把最富裕的免费额度花掉。
// v1 口径：四窗口最紧余量（无定价数据，接入 registry 定价后升级）。
type cheapStrategy struct{ q *QuotaTracker }

func (cheapStrategy) Name() string { return "cheap" }

func (c cheapStrategy) Order(cands []provider.Provider, _ *RouteContext) []provider.Provider {
	if c.q == nil {
		return cands
	}
	sorted := append([]provider.Provider(nil), cands...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return c.q.Headroom(sorted[i].Name()) > c.q.Headroom(sorted[j].Name())
	})
	return sorted
}

// fastStrategy 按延迟 EWMA 升序：未观测过的 key 视为 0ms（乐观探索）。
type fastStrategy struct{ s *Scorer }

func (fastStrategy) Name() string { return "fast" }

func (f fastStrategy) Order(cands []provider.Provider, _ *RouteContext) []provider.Provider {
	if f.s == nil {
		return cands
	}
	sorted := append([]provider.Provider(nil), cands...)
	sort.SliceStable(sorted, func(i, j int) bool {
		mi, _ := f.s.Snapshot(sorted[i].Name())
		mj, _ := f.s.Snapshot(sorted[j].Name())
		return mi < mj
	})
	return sorted
}

// lkgpStrategy（last known good provider）最近成功者优先：谁刚成功过
// 谁排最前，从未成功者殿后；同组内保持注册序。粘会话语义由 2.7 接入。
type lkgpStrategy struct{ s *Scorer }

func (lkgpStrategy) Name() string { return "lkgp" }

func (l lkgpStrategy) Order(cands []provider.Provider, _ *RouteContext) []provider.Provider {
	if l.s == nil {
		return cands
	}
	sorted := append([]provider.Provider(nil), cands...)
	sort.SliceStable(sorted, func(i, j int) bool {
		ti, oki := l.s.LastSuccessAt(sorted[i].Name())
		tj, okj := l.s.LastSuccessAt(sorted[j].Name())
		if oki != okj {
			return oki // 成功过的在前
		}
		return ti.After(tj) // 更近的成功在前
	})
	return sorted
}
