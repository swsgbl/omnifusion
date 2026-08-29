// window.go 是 M4.5 跨层接口（docs/04 §4.3 跨层协同）：L4 压缩管线
// 输出的压缩后 token 估算喂给 L3——窗口装不下的候选模型被排除出尝试
// 序列；压缩把请求缩小后，原本装不下的小上下文（免费层）模型进入
// 候选。窗口是硬边界但保守：目录未收录的模型不过滤，全部候选都
// 装不下时回退未过滤列表（宁可让上游拒绝，不能全拒）。
package routing

import (
	"github.com/swsgbl/omnifusion/internal/provider"
)

// WindowResolver 供给 (provider, model) 的上下文窗口（token）。
// 生产实现是 Catalog（live 目录 + 静态回落）；第二返回值 false 表示
// 目录未收录，调用方不过滤。
type WindowResolver interface {
	ContextWindow(providerName, model string) (int64, bool)
}

// filterByWindow 排除窗口装不下 tokens 的候选。cands 不会被就地
// 修改（可能就是 r.Providers 本体）；全排除时原样返回（保守回退）。
func (r *Router) filterByWindow(cands []provider.Provider, tokens int64, model string) []provider.Provider {
	if r.Windows == nil || tokens <= 0 || model == "" {
		return cands
	}
	kept := make([]provider.Provider, 0, len(cands))
	for _, p := range cands {
		if w, ok := r.Windows.ContextWindow(p.Name(), model); ok && w > 0 && w < tokens {
			continue // 目录明确装不下：排除
		}
		kept = append(kept, p)
	}
	if len(kept) == 0 {
		return cands
	}
	return kept
}

// candidates 计算一次分发的最终尝试序列：组合路径（M4.7）走成员
// 声明序；普通路径策略/打分排序 → 模型成员过滤（docs/00 §4.5，
// 排除目录明确不服务该模型的家——绑定到不可服务家的 sticky 无意义，
// 故过滤在前）→ sticky 提前 → 窗口过滤（cfg.promptTokens 为 L4
// 压缩后 token）→ 钉选提前（M5.2，运维显式意图压过 sticky）。
// 每个候选携带实际发往上游的模型名（组合成员模型 / 裸模型名）。
func (r *Router) candidates(cfg dispatchConfig, model string) []candidate {
	if cfg.comboName != "" {
		return r.comboCandidates(cfg)
	}
	cands := r.filterByModel(r.ordered(cfg.strategyName, model, cfg.promptTokens), model)
	cands = r.applySticky(cands, cfg.sessionID)
	kept := r.filterByWindow(cands, cfg.promptTokens, model)
	kept = r.applyPin(kept, cfg.pinnedProvider)
	out := make([]candidate, 0, len(kept))
	for _, p := range kept {
		out = append(out, candidate{p: p, model: model})
	}
	return out
}

// applyPin 把钉选 provider 提到候选首位（M5.2 全局路由钉选）：不在
// 候选中则原样返回（钉选不引入新候选——没装配的 provider 钉了也白钉）。
func (r *Router) applyPin(cands []provider.Provider, pinned string) []provider.Provider {
	if pinned == "" || len(cands) < 2 {
		return cands
	}
	idx := -1
	for i, p := range cands {
		if p.Name() == pinned {
			idx = i
			break
		}
	}
	if idx <= 0 { // 不在场或已在首位
		return cands
	}
	out := make([]provider.Provider, 0, len(cands))
	out = append(out, cands[idx])
	out = append(out, cands[:idx]...)
	out = append(out, cands[idx+1:]...)
	return out
}
