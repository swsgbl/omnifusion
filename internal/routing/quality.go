// quality.go 是「按能力由强到弱」策略（2026-08-29 补齐，
// 冻结接口注释自第一天就预留的 quality 名）：候选按社区 feed 的模型
// 能力分（0-100）降序尝试——把「免费模型路由器」的能力排序半边补上。
// 数据源是 签名 feed 的 capability 字段（Ed25519 验签 + 防回滚，
// 随 feed 版本演进）；无 feed/未评级的候选得 0 分殿后且同分保持注册序
// （质量数据缺失时退化为 priority 语义，不确定不惩罚可用性——隔离/
// 配额/窗口/成员过滤等硬边界在本策略下照常先行短路）。
package routing

import (
	"sort"

	"github.com/swsgbl/omnifusion/internal/provider"
)

// CapabilityResolver 是 (provider, model) → 能力分 的查询面（生产装配
// 为 Catalog，feed 是数据源）。ok=false 表示未评级（按 0 分处理）。
type CapabilityResolver interface {
	Capability(providerName, model string) (float64, bool)
}

// qualityStrategy 按请求模型在各候选处的能力分降序排（稳定排序）。
type qualityStrategy struct {
	cap CapabilityResolver
}

func (qualityStrategy) Name() string { return "quality" }

func (q qualityStrategy) Order(cands []provider.Provider, rc *RouteContext) []provider.Provider {
	if q.cap == nil || rc == nil || rc.Model == "" {
		return cands // 无能力数据源或无目标模型：保持注册序
	}
	sorted := append([]provider.Provider(nil), cands...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return capabilityOf(q.cap, sorted[i].Name(), rc.Model) >
			capabilityOf(q.cap, sorted[j].Name(), rc.Model)
	})
	return sorted
}

// capabilityOf 查询候选能力分（未评级 = 0）。
func capabilityOf(c CapabilityResolver, providerName, model string) float64 {
	if v, ok := c.Capability(providerName, model); ok {
		return v
	}
	return 0
}

// BestModelResolver 按 provider 取能力最强的模型（裸 "@quality" 的
// 自动选模依据；生产装配 Catalog 双实现）。
type BestModelResolver interface {
	BestModel(providerName string) (string, float64, bool)
}

// qualityCandidates 是裸 "@quality"（自动选最强）的候选生成：每家取
// 其最强模型，按能力分降序逐尝试（复用 combo 的逐候选改写机制——
// 候选 model 即该家最强模型）；无 feed 数据的 provider 跳过（不能
// 凭空选模型），sticky/钉选/窗口过滤不介入（逐请求语义，与 @smart
// 同哲学：自动选强每次都按当前数据裁决）。
func (r *Router) qualityCandidates(cfg dispatchConfig) []candidate {
	bm, ok := r.Capability.(BestModelResolver)
	if !ok || bm == nil {
		if r.Log != nil {
			r.Log.Warn("quality auto requires a capability source; none configured")
		}
		return nil
	}
	type scored struct {
		p   provider.Provider
		mdl string
		cap float64
	}
	list := make([]scored, 0, len(r.Providers))
	for _, p := range r.Providers {
		id, cap, ok := bm.BestModel(p.Name())
		if !ok {
			continue
		}
		list = append(list, scored{p: p, mdl: id, cap: cap})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].cap > list[j].cap })
	out := make([]candidate, 0, len(list))
	for _, s := range list {
		out = append(out, candidate{p: s.p, model: s.mdl})
	}
	return r.filterByWindowC(out, cfg.promptTokens)
}
