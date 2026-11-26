// catalog_feed.go 是 签名 feed 在目录层的落点：已验签 feed 的
// 上下文窗口与众测状态并入 Catalog。核心价值在窗口补齐——provider
// 的 /models 清单几乎不报 context_length，注册表静态值覆盖有限，
// 社区维护的 feed 是窗口过滤数据的第三来源；probation 状态
// 只做观测标注，不参与路由决策（众测协议：降级裁决靠发新 feed）。
package routing

import (
	"sort"
	"strings"

	"github.com/swsgbl/omnifusion/internal/catalogfeed"
	"github.com/swsgbl/omnifusion/internal/provider"
)

// FeedModelEntry 是 feed 视角的一个模型（观测/report 面）。
type FeedModelEntry struct {
	Provider  string
	ID        string
	CtxLen    int64
	Probation bool
}

// ApplyFeed 并入已验签 feed（cmd/ofd 在 Ingest 成功后调用），返回
// 并入条目数。同版本重复应用幂等（整组覆盖写）。
func (c *Catalog) ApplyFeed(f *catalogfeed.Feed) int {
	if f == nil {
		return 0
	}
	wins := map[string]map[string]int64{}
	prob := map[string]map[string]bool{}
	caps := map[string]map[string]float64{}
	prices := map[string]map[string]provider.Price{}
	n := 0
	for name, pf := range f.Providers {
		for _, m := range pf.Models {
			if wins[name] == nil {
				wins[name] = map[string]int64{}
			}
			if prob[name] == nil {
				prob[name] = map[string]bool{}
			}
			if caps[name] == nil {
				caps[name] = map[string]float64{}
			}
			wins[name][m.ID] = m.CtxLen
			prob[name][m.ID] = m.Status == catalogfeed.StatusProbation
			caps[name][m.ID] = m.Capability
			if m.PriceIn != nil && m.PriceOut != nil {
				if prices[name] == nil {
					prices[name] = map[string]provider.Price{}
				}
				prices[name][m.ID] = provider.Price{In: *m.PriceIn, Out: *m.PriceOut}
			}
			n++
		}
	}
	c.mu.Lock()
	c.feedWindows = wins
	c.feedProbation = prob
	c.feedCapability = caps
	c.feedPrices = prices
	c.mu.Unlock()
	return n
}

// Capability 返回 (provider, model) 的社区能力分（0-100，quality
// 策略排序依据）。匹配口径与 ServesModel 一致：精确 id 优先，其次
// OpenRouter 风格 "厂商/模型" 后缀；无 feed 或未收录返回 ok=false
// （策略按未评级处理，不阻断分发）。
func (c *Catalog) Capability(providerName, model string) (float64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	caps := c.feedCapability[providerName]
	if v, ok := caps[model]; ok {
		return v, true
	}
	for id, v := range caps {
		if strings.HasSuffix(id, "/"+model) {
			return v, true
		}
	}
	return 0, false
}

// BestModel 返回该 provider 能力分最高的模型（裸 @quality 的自动
// 选模依据）。只在"此刻能服务的清单"里选：live 快照优先，无快照回落
// 静态声明，两者都没有才信 feed 全集。feed 是按版本冻结的，上游
// 轮换后常留着已下线的条目——2026-11-25 用户实撞：NVIDIA 的 feed 首选
// 早已 410，裸 @quality 每个请求都先去撞它。同分按 id 取小（map 遍历
// 序随机，不能让同一份目录每次选出不同模型）。清单里有能力的模型
// 一个都没有时返回 ok=false（不能凭空选模型）。
func (c *Catalog) BestModel(providerName string) (string, float64, bool) {
	c.mu.RLock()
	caps := c.feedCapability[providerName]
	pool := c.serveable(providerName)
	hasPool := len(pool) > 0
	c.mu.RUnlock()
	best, bestCap := "", float64(-1)
	if hasPool {
		for _, m := range pool {
			v, ok := caps[m.ID]
			if !ok {
				continue
			}
			if betterPick(m.ID, v, best, bestCap) {
				best, bestCap = m.ID, v
			}
		}
	} else {
		for id, v := range caps {
			if betterPick(id, v, best, bestCap) {
				best, bestCap = id, v
			}
		}
	}
	if best == "" {
		return "", 0, false
	}
	return best, bestCap, true
}

// betterPick 是 BestModel 的择优规则：分高者胜，同分 id 小者胜。
func betterPick(id string, v float64, best string, bestCap float64) bool {
	if best == "" || v > bestCap {
		return true
	}
	return v == bestCap && id < best
}

// FeedProbation 返回 (provider, model) 是否处于众测期（feed 标注；
// 无 feed 或未收录返回 false）。
func (c *Catalog) FeedProbation(providerName, model string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.feedProbation[providerName][model]
}

// FeedSnapshot 返回 feed 当前覆盖的平铺视图（按 provider、id 排序；
// 观测/测试用）。
func (c *Catalog) FeedSnapshot() []FeedModelEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]FeedModelEntry, 0, 32)
	for name, models := range c.feedWindows {
		for id, ctx := range models {
			out = append(out, FeedModelEntry{
				Provider: name, ID: id, CtxLen: ctx,
				Probation: c.feedProbation[name][id],
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].ID < out[j].ID
	})
	return out
}
