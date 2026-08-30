// combo.go 是 命名模型组（路由组合）：有序 (provider, model)
// 成员 + 绑定的压缩组合名（per-path 压缩策略，「压缩组合可绑定
// 到路由组合，免费层路径用激进压缩」）。请求经 model 内嵌 "@combo:NAME"
// 指令选择；成员声明序即尝试优先级，逐尝试把 req.Model 改写为成员模型。
package routing

import (
	"github.com/swsgbl/omnifusion/internal/provider"
)

// ComboMember 是路由组合的一个成员：发往 provider 的 model 由成员
// 声明（combo 是跨家模型组的真相源，不依赖 provider 侧别名表）。
type ComboMember struct {
	Provider string
	Model    string
}

// Combo 是命名模型组。Compression 是绑定的压缩组合名（可空 = 纯
// 路由组合，不压缩）；组合定义来自 YAML（cmd/ofd 装配注入）。
type Combo struct {
	Name        string
	Members     []ComboMember
	Compression string
}

// Combo 取命名组合；第二返回值 false 表示未定义。
func (r *Router) Combo(name string) (Combo, bool) {
	c, ok := r.Combos[name]
	return c, ok
}

// providerByName 按名找已装配 provider；未装配返回 nil。
func (r *Router) providerByName(name string) provider.Provider {
	for _, p := range r.Providers {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

// candidate 是一次分发尝试的执行单元：目标 provider 与实际发往
// 上游的模型名（组合成员模型；普通路径为裸模型名）。逐尝试改写
// req.Model 的责任在 Dispatch/DispatchStream。
type candidate struct {
	p     provider.Provider
	model string
}

// comboCandidates 解析组合成员为尝试序列：声明序（=用户优先级）→
// sticky 提前 → 按成员模型逐个窗口过滤。未装配的成员 provider 跳过
// 并记日志（组合是静态配置，环境里少一家 key 不该废掉整个组合）。
func (r *Router) comboCandidates(cfg dispatchConfig) []candidate {
	combo, ok := r.Combos[cfg.comboName]
	if !ok {
		return nil
	}
	out := make([]candidate, 0, len(combo.Members))
	for _, m := range combo.Members {
		p := r.providerByName(m.Provider)
		if p == nil {
			if r.Log != nil {
				r.Log.Warn("combo member provider not configured; skipping",
					"combo", combo.Name, "provider", m.Provider)
			}
			continue
		}
		out = append(out, candidate{p: p, model: m.Model})
	}
	out = r.applyStickyC(out, cfg.sessionID)
	return r.filterByWindowC(out, cfg.promptTokens)
}

// applyStickyC 是 combo 形 sticky：绑定 provider 提前（与 sessions.go
// 的 applySticky 同语义，作用于带成员模型的候选）。
func (r *Router) applyStickyC(cands []candidate, sessionID string) []candidate {
	if r.Sessions == nil || sessionID == "" || len(cands) <= 1 {
		return cands
	}
	bound, ok := r.Sessions.Bound(sessionID)
	if !ok {
		return cands
	}
	for i, c := range cands {
		if c.p.Name() != bound {
			continue
		}
		out := make([]candidate, 0, len(cands))
		out = append(out, c)
		out = append(out, cands[:i]...)
		return append(out, cands[i+1:]...)
	}
	return cands
}

// filterByWindowC 是 combo 形窗口过滤：逐候选查 (provider, 成员模型)
// 的上下文窗口（与 window.go 的 filterByWindow 同语义；全排除回退
// 原列表保守不拒）。
func (r *Router) filterByWindowC(cands []candidate, tokens int64) []candidate {
	if r.Windows == nil || tokens <= 0 {
		return cands
	}
	kept := make([]candidate, 0, len(cands))
	for _, c := range cands {
		if w, ok := r.Windows.ContextWindow(c.p.Name(), c.model); ok && w > 0 && w < tokens {
			continue
		}
		kept = append(kept, c)
	}
	if len(kept) == 0 {
		return cands
	}
	return kept
}
