// smart.go 是 ML 路由路径（学习型模型不进默认二进制）：@smart 指令
// 把候选选择交给逐请求难度分类。Router.Smart 是注入的计划函数（cmd/ofd
// 从 intelligence.MLRouter 适配而来；L5 与 L3 互不 import，与 
// DispatchFunc 同一注入模式）。计划成员即尝试序列：主档在前、另一档
// 殿后作 failover；不做 sticky/钉选（逐请求分类是本意——上一问简单
// 不代表下一问也简单）；窗口过滤照常（难度高 ≠ 装得下）。隔离/配额/
// 打分在 Dispatch/DispatchStream 主循环照常生效。
package routing

import (
	"errors"
	"sort"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
)

// SmartPlan 是一次 ML 路由的候选计划（Router.Smart 的产出契约）。
type SmartPlan struct {
	// Members 是尝试序列（主档在前、另一档殿后作 failover）。
	Members []ComboMember
	// Tier 是命中档位（"weak"/"strong"；观测/日志面）。
	Tier string
	// Difficulty 是难度分 0..1（观测面）。
	Difficulty float64
}

// WithSmart 选择 ML 路由（"@smart" 指令，server 边界转换；
// Router.Smart 未装配时回退普通分发并告警——边界已拦 400，此处兜底
// 保证路由层自洽）。
func WithSmart() DispatchOption {
	return func(c *dispatchConfig) { c.smart = true }
}

// WithQualityAuto 选择裸 "@quality"（自动选最强，2026-08-29）：候选按
// 各 provider 的 BestModel 能力分降序，逐尝试把模型改写为该家最强
// 模型——「免费模型路由器」对小白的一键形态。
func WithQualityAuto() DispatchOption {
	return func(c *dispatchConfig) { c.qualityAuto = true }
}

// candidatesFor 统一 Dispatch/DispatchStream 的候选入口：smart 指令
// 走 ML 计划，裸 @quality 走自动选强，其余按 model 走普通/组合路径。
// qualityAuto 产出空候选（无能力数据源）是可预期边界，返回哨兵错误
// 让两个分发入口统一给一句可行动的报错，而不是"no attempts"天书。
var errQualityNoData = errors.New("routing: auto mode needs catalog capability data (none available); pick a specific model and retry")

func (r *Router) candidatesFor(cfg dispatchConfig, req *schema.UnifiedRequest) ([]candidate, error) {
	if cfg.smart {
		return r.smartCandidates(cfg, req), nil
	}
	if cfg.qualityAuto {
		cands := r.qualityCandidates(cfg)
		if len(cands) == 0 {
			return nil, errQualityNoData
		}
		return cands, nil
	}
	// 裸 @cheap（无目标模型）：每家取其登记最低价模型逐尝试（镜像
	// qualityAuto 的自动选模——只排序不改写会把空模型名发给上游）。
	if cfg.strategyName == "cheap" && req.Model == "" {
		if r.Price == nil {
			return nil, errors.New("routing: @cheap auto needs declared prices (none available); pick a specific model and retry")
		}
		cands := r.cheapCandidates(cfg)
		if len(cands) == 0 {
			return nil, errors.New("routing: @cheap auto found no declared prices; pick a specific model and retry")
		}
		return cands, nil
	}
	return r.candidates(cfg, req.Model), nil
}

// cheapCandidates 是裸 "@cheap" 的候选生成：每家取其登记最低价模型，
// 按三档排序——登记免费（0 价，余量降序）→ 已定价（价升序）。无任何
// 登记价的 provider 跳过（不能凭空选模型，与 qualityAuto 同语义）。
func (r *Router) cheapCandidates(cfg dispatchConfig) []candidate {
	type scored struct {
		p    provider.Provider
		model string
		cost float64
		free bool
		head float64
	}
	list := make([]scored, 0, len(r.Providers))
	for _, p := range r.Providers {
		id, price, ok := r.Price.CheapestModel(p.Name())
		if !ok {
			continue
		}
		var head float64
		if r.Quota != nil {
			head = r.Quota.Headroom(p.Name())
		}
		list = append(list, scored{
			p: p, model: id, cost: price.In + price.Out,
			free: price.In <= 0 && price.Out <= 0, head: head,
		})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].free != list[j].free {
			return list[i].free // 免费档在前
		}
		if !list[i].free && list[i].cost != list[j].cost {
			return list[i].cost < list[j].cost // 付费按价升序
		}
		return list[i].head > list[j].head // 同档余量降序
	})
	out := make([]candidate, 0, len(list))
	for _, s := range list {
		out = append(out, candidate{p: s.p, model: s.model})
	}
	return r.filterByWindowC(out, cfg.promptTokens)
}

// smartCandidates 把 ML 计划解析为尝试序列：未装配成员 provider 跳过
// 并告警（与 comboCandidates 同语义：静态配置，环境里少一家 key 不该
// 废掉整条路径）；Smart 未装配（nil）回退普通分发。
func (r *Router) smartCandidates(cfg dispatchConfig, req *schema.UnifiedRequest) []candidate {
	if r.Smart == nil {
		if r.Log != nil {
			r.Log.Warn("smart routing requested but ML router not configured; falling back to model routing")
		}
		return r.candidates(cfg, req.Model)
	}
	plan := r.Smart(req)
	out := make([]candidate, 0, len(plan.Members))
	for _, m := range plan.Members {
		p := r.providerByName(m.Provider)
		if p == nil {
			if r.Log != nil {
				r.Log.Warn("smart plan member provider not configured; skipping",
					"provider", m.Provider, "tier", plan.Tier)
			}
			continue
		}
		out = append(out, candidate{p: p, model: m.Model})
	}
	return r.filterByWindowC(out, cfg.promptTokens)
}
