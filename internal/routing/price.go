// price.go 是 cheap 策略的真成本升级（登记定价 → 真成本路由）：注册表
// 与签名 feed 登记每模型定价（USD / 1M tokens；显式 0 = 免费声明，
// 省略 = 未登记）。cheap 候选按预计请求成本分三档排——登记免费 →
// 未登记 → 已定价按成本升序；档内沿用 v1 的配额余量降序（免费额度
// 也是先花最富裕的）。无定价数据源时整体退回 v1 纯余量语义
//（不确定不惩罚可用性；隔离/配额/窗口/成员过滤等硬边界照常先行）。
package routing

import (
	"sort"
	"strings"

	"github.com/swsgbl/omnifusion/internal/provider"
)

// nominalOutTokens 是成本估算的输出侧名义长度：真实输出长度请求前
// 未知，排序只需跨候选一致的单调口径（输入侧用压缩后 token 估算）。
const nominalOutTokens = 1024

// PriceResolver 是 cheap 策略的定价查询面（生产装配为 Catalog：
// 签名 feed 优先，注册表静态声明兜底）。ok=false 表示未登记。
type PriceResolver interface {
	// Price 返回 (provider, model) 的登记定价；匹配口径与
	// Capability 一致（精确 id 优先，其次 "厂商/模型" 后缀）。
	Price(providerName, model string) (provider.Price, bool)
	// Cheapest 返回该 provider 登记定价最低的模型价格（裸 "@cheap"
	// 无目标模型时按各家最低价排）。
	Cheapest(providerName string) (provider.Price, bool)
}

// cheapStrategy 按真成本升序（v2，登记定价后升级）：三档——0 档
// 登记免费（成本 0）；1 档未登记价格（不确定不惩罚）；2 档已定价
//（预计成本升序）。档内/同价按配额余量降序（v1 语义：免费额度
// 先花最富裕的）。
type cheapStrategy struct {
	q  *QuotaTracker
	pr PriceResolver
}

func (cheapStrategy) Name() string { return "cheap" }

func (c cheapStrategy) Order(cands []provider.Provider, rc *RouteContext) []provider.Provider {
	if c.pr == nil {
		return orderCheapByHeadroom(cands, c.q) // 无定价源：v1 纯余量语义
	}
	sorted := append([]provider.Provider(nil), cands...)
	sort.SliceStable(sorted, func(i, j int) bool {
		ti, ci, hi := cheapRank(c.pr, c.q, sorted[i], rc)
		tj, cj, hj := cheapRank(c.pr, c.q, sorted[j], rc)
		if ti != tj {
			return ti < tj
		}
		if ti == 2 && ci != cj {
			return ci < cj
		}
		return hi > hj
	})
	return sorted
}

// cheapRank 折算候选的 (档位, 成本, 余量)：登记 0 价 → 0 档；未登记
// → 1 档；已定价 → 2 档（成本 = 输入单价×压缩后 token + 输出单价×
// 名义输出长度；常数缩放对排序无影响）。
func cheapRank(pr PriceResolver, q *QuotaTracker, p provider.Provider, rc *RouteContext) (tier int, cost, headroom float64) {
	if q != nil {
		headroom = q.Headroom(p.Name())
	}
	price, ok := priceOf(pr, p.Name(), rc)
	if !ok {
		return 1, 0, headroom
	}
	if price.In <= 0 && price.Out <= 0 {
		return 0, 0, headroom // 登记免费
	}
	inTok := 0.0
	if rc != nil && rc.PromptTokens > 0 {
		inTok = float64(rc.PromptTokens)
	}
	return 2, price.In*inTok + price.Out*nominalOutTokens, headroom
}

// priceOf 取候选的排序定价：有目标模型查精确价（成员过滤已把不供
// 该模型的家剔除），无目标模型取该家登记最低价。
func priceOf(pr PriceResolver, name string, rc *RouteContext) (provider.Price, bool) {
	if rc != nil && rc.Model != "" {
		return pr.Price(name, rc.Model)
	}
	return pr.Cheapest(name)
}

// orderCheapByHeadroom 是 v1 语义（无定价源回退）：四窗口最紧余量降序。
func orderCheapByHeadroom(cands []provider.Provider, q *QuotaTracker) []provider.Provider {
	if q == nil {
		return cands
	}
	sort.SliceStable(cands, func(i, j int) bool {
		return q.Headroom(cands[i].Name()) > q.Headroom(cands[j].Name())
	})
	return cands
}

// Price 返回 (provider, model) 的登记定价：签名 feed 优先（社区维护、
// 可随版本更新价格），注册表静态声明兜底；都无返回 ok=false。
func (c *Catalog) Price(providerName, model string) (provider.Price, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if p, ok := lookupPrice(c.feedPrices[providerName], model); ok {
		return p, true
	}
	return lookupPrice(c.staticPrices[providerName], model)
}

// Cheapest 返回该 provider 登记定价最低的模型价格（feed 与静态合并
// 取 In+Out 最低；无任何登记返回 ok=false）。
func (c *Catalog) Cheapest(providerName string) (provider.Price, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	merged := map[string]provider.Price{}
	for id, p := range c.staticPrices[providerName] {
		merged[id] = p
	}
	for id, p := range c.feedPrices[providerName] {
		merged[id] = p
	}
	var best provider.Price
	found := false
	for _, p := range merged {
		if !found || p.In+p.Out < best.In+best.Out {
			best, found = p, true
		}
	}
	return best, found
}

// lookupPrice 在一个价格索引内做精确/后缀匹配（口径同 Capability）。
func lookupPrice(idx map[string]provider.Price, model string) (provider.Price, bool) {
	if p, ok := idx[model]; ok {
		return p, true
	}
	for id, p := range idx {
		if strings.HasSuffix(id, "/"+model) {
			return p, true
		}
	}
	return provider.Price{}, false
}
