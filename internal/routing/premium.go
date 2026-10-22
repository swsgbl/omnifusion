// premium.go 是「付费优先」策略（magpie 借鉴融合轮，用户 2026-10-22
// 需求："单独付费的选项/付费优先"）：已登记付费价的候选排前（档内按
// 能力分降序——付费的意图是质量），免费池殿后作兜底；每日预算
//（Router.DailyBudgetUSD>0 且 SpendToday() 已超额）时整序反转成
// 免费优先——付费优先但不失控，免费池永远是安全网。cheap 的镜像：
// cheap 三档免费→未登记→付费升序，premium 付费→未登记→免费。
package routing

import (
	"sort"

	"github.com/swsgbl/omnifusion/internal/provider"
)

// premiumStrategy 按付费优先排候选：档位 0=已登记付费价（能力分降
// 序）→ 1=未登记 → 2=登记免费；预算耗尽时反转（0=免费 → … → 2=付费
// 殿后，档内序不变）。
type premiumStrategy struct {
	pr     PriceResolver
	cap    CapabilityResolver
	spend  func() float64
	budget float64
}

func (premiumStrategy) Name() string { return "premium" }

func (p premiumStrategy) Order(cands []provider.Provider, rc *RouteContext) []provider.Provider {
	if p.pr == nil {
		return cands // 无定价源：无从谈付费优先，保持注册序
	}
	degraded := p.budgetExhausted()
	sorted := append([]provider.Provider(nil), cands...)
	sort.SliceStable(sorted, func(i, j int) bool {
		ti, ci := premiumRank(p.pr, p.cap, sorted[i], rc, degraded)
		tj, cj := premiumRank(p.pr, p.cap, sorted[j], rc, degraded)
		if ti != tj {
			return ti < tj
		}
		return ci > cj // 档内能力分降序（付费档=质量意图；免费兜底档同理）
	})
	return sorted
}

// budgetExhausted 判定今日预算是否已花完（未设预算恒 false）。
func (p premiumStrategy) budgetExhausted() bool {
	if p.budget <= 0 || p.spend == nil {
		return false
	}
	return p.spend() >= p.budget
}

// premiumRank 折算候选档位与能力分：付费价（In/Out 任一 >0）=0 档；
// 未登记=1；登记免费=2。预算耗尽时 0↔2 反转（未登记居中不变）。
func premiumRank(pr PriceResolver, cap CapabilityResolver, p provider.Provider,
	rc *RouteContext, degraded bool) (tier int, capability float64) {
	if cap != nil && rc != nil && rc.Model != "" {
		capability = capabilityOf(cap, p.Name(), rc.Model)
	}
	price, ok := priceOf(pr, p.Name(), rc)
	if !ok {
		return 1, capability
	}
	tier = 2
	if price.In > 0 || price.Out > 0 {
		tier = 0
	}
	if degraded {
		tier = 2 - tier // 0↔2 反转：免费升到 0 档，付费降到 2 档
	}
	return tier, capability
}

// premiumCandidates 是裸 "@premium"（自动选模）的候选生成：每家取其
// 最强模型（BestModel，同 qualityAuto 约束），按"该家最强模型是否登记
// 付费价"分档——付费家在前、免费家殿后，档内能力分降序；预算耗尽时
// 反转。窗口过滤照常。
func (r *Router) premiumCandidates(cfg dispatchConfig) []candidate {
	bm, ok := r.Capability.(BestModelResolver)
	if !ok || bm == nil {
		if r.Log != nil {
			r.Log.Warn("premium auto requires a capability source; none configured")
		}
		return nil
	}
	degraded := r.budgetDegraded()
	type scored struct {
		p    provider.Provider
		mdl  string
		cap  float64
		tier int
	}
	list := make([]scored, 0, len(r.Providers))
	for _, p := range r.Providers {
		id, capScore, ok := bm.BestModel(p.Name())
		if !ok {
			continue
		}
		tier := 1
		if r.Price != nil {
			if price, ok := r.Price.Price(p.Name(), id); ok {
				tier = 2
				if price.In > 0 || price.Out > 0 {
					tier = 0
				}
			}
		}
		if degraded {
			tier = 2 - tier
		}
		list = append(list, scored{p: p, mdl: id, cap: capScore, tier: tier})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].tier != list[j].tier {
			return list[i].tier < list[j].tier
		}
		return list[i].cap > list[j].cap
	})
	out := make([]candidate, 0, len(list))
	for _, s := range list {
		out = append(out, candidate{p: s.p, model: s.mdl})
	}
	return r.filterByWindowC(out, cfg.promptTokens)
}

// budgetDegraded 是 Router 层的预算耗尽判定（供裸 @premium 候选生成；
// 策略路径经 premiumStrategy.budgetExhausted，同一份字段）。
func (r *Router) budgetDegraded() bool {
	if r.DailyBudgetUSD <= 0 || r.SpendToday == nil {
		return false
	}
	return r.SpendToday() >= r.DailyBudgetUSD
}
