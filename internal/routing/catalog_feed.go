// catalog_feed.go 是 M6.5 签名 feed 在目录层的落点：已验签 feed 的
// 上下文窗口与众测状态并入 Catalog。核心价值在窗口补齐——provider
// 的 /models 清单几乎不报 context_length，注册表静态值覆盖有限，
// 社区维护的 feed 是窗口过滤（M4.5）数据的第三来源；probation 状态
// 只做观测标注，不参与路由决策（众测协议：降级裁决靠发新 feed）。
package routing

import (
	"sort"
	"strings"

	"github.com/swsgbl/omnifusion/internal/catalogfeed"
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
			n++
		}
	}
	c.mu.Lock()
	c.feedWindows = wins
	c.feedProbation = prob
	c.feedCapability = caps
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
// 选模依据）。无 feed 数据返回 ok=false。优先返回 live/静态清单里
// 实际在列的模型（分数并列时），避免推荐目录里不存在的模型。
func (c *Catalog) BestModel(providerName string) (string, float64, bool) {
	c.mu.RLock()
	caps := c.feedCapability[providerName]
	live := c.models[providerName]
	c.mu.RUnlock()
	best, bestCap := "", float64(-1)
	for id, cap := range caps {
		if cap > bestCap {
			best, bestCap = id, cap
		}
	}
	if best == "" {
		return "", 0, false
	}
	// live 清单里有更高分的可服务模型吗？（feed 可能收录了已下线条目）
	for _, m := range live {
		if v, ok := caps[m.ID]; ok && v > bestCap {
			best, bestCap = m.ID, v
		}
	}
	return best, bestCap, true
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
