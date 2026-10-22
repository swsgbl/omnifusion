// costledger.go 是成本账本（magpie 借鉴融合轮）：按登记定价把每次
// 请求的 token 用量折算成美元估算（USD = inTok×Price.In/1e6 +
// outTok×Price.Out/1e6），按日聚合（provider/model/总量）持久化于
// store meta 表（key 形如 spend:2026-10-22）——@premium 的预算护栏
// 与 usage 页成本列共用同一份事实。缓存命中不记账（上游未发生花费）；
// 未登记定价的模型记 0（免费池语义）。
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/store"
)

// costDay 是一天的聚合面（meta 值形态）。
type costDay struct {
	Total      float64            `json:"total"`
	ByProvider map[string]float64 `json:"by_provider,omitempty"`
	Requests   int64              `json:"requests"`
}

// CostLedger 是日粒度成本账本：内存聚合 + 写透 meta 表。
// 定价查询经注入的 priceFn（生产装配=Router.Price/Catalog）。
type CostLedger struct {
	mu     sync.Mutex
	st     *store.Store
	log    *slog.Logger
	priceF func(providerName, model string) (provider.Price, bool)
	day    string
	cur    costDay
}

// NewCostLedger 构造账本并载入今日既有聚合（重启不丢当日成本）。
func NewCostLedger(st *store.Store, log *slog.Logger,
	priceF func(providerName, model string) (provider.Price, bool)) *CostLedger {
	l := &CostLedger{st: st, log: log, priceF: priceF, day: todayKey()}
	l.cur = l.loadDay(l.day)
	return l
}

// Estimate 按登记定价折算一次请求的美元成本（未登记=0）。
func (l *CostLedger) Estimate(providerName, model string, inTok, outTok int) float64 {
	if l == nil || l.priceF == nil {
		return 0
	}
	price, ok := l.priceF(providerName, model)
	if !ok {
		return 0
	}
	return float64(inTok)*price.In/1e6 + float64(outTok)*price.Out/1e6
}

// Record 记一次已花费请求（roll 日键；写透持久化，失败仅告警不阻断）。
func (l *CostLedger) Record(providerName, model string, inTok, outTok int) {
	if l == nil {
		return
	}
	usd := l.Estimate(providerName, model, inTok, outTok)
	l.mu.Lock()
	defer l.mu.Unlock()
	if day := todayKey(); day != l.day {
		l.day, l.cur = day, costDay{ByProvider: map[string]float64{}}
	}
	l.cur.Total += usd
	l.cur.Requests++
	if l.cur.ByProvider == nil {
		l.cur.ByProvider = map[string]float64{}
	}
	l.cur.ByProvider[providerName] += usd
	l.persist()
}

// TodayTotal 返回今日累计成本（预算护栏查询面）。
func (l *CostLedger) TodayTotal() float64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if todayKey() != l.day { // 跨日未触发 roll（长期空闲后首查）
		l.day, l.cur = todayKey(), costDay{ByProvider: map[string]float64{}}
		l.persist()
	}
	return l.cur.Total
}

// TodaySnapshot 返回今日聚合的副本（usage API 面）。
func (l *CostLedger) TodaySnapshot() (total float64, byProvider map[string]float64, requests int64) {
	if l == nil {
		return 0, nil, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if todayKey() != l.day {
		l.day, l.cur = todayKey(), costDay{ByProvider: map[string]float64{}}
		l.persist()
	}
	bp := make(map[string]float64, len(l.cur.ByProvider))
	for k, v := range l.cur.ByProvider {
		bp[k] = v
	}
	return l.cur.Total, bp, l.cur.Requests
}

// persist 写透 meta 表（调用方持锁）。
func (l *CostLedger) persist() {
	if l.st == nil {
		return
	}
	raw, err := json.Marshal(l.cur)
	if err != nil {
		return
	}
	if err := l.st.SetMeta("spend:"+l.day, string(raw)); err != nil && l.log != nil {
		l.log.Warn("cost ledger persist", "err", err)
	}
}

// loadDay 读回某日聚合（无记录返回零值）。
func (l *CostLedger) loadDay(day string) costDay {
	if l.st == nil {
		return costDay{ByProvider: map[string]float64{}}
	}
	v, err := l.st.GetMeta("spend:" + day)
	if err != nil || v == "" {
		return costDay{ByProvider: map[string]float64{}}
	}
	var d costDay
	if err := json.Unmarshal([]byte(v), &d); err != nil {
		return costDay{ByProvider: map[string]float64{}}
	}
	if d.ByProvider == nil {
		d.ByProvider = map[string]float64{}
	}
	return d
}

// todayKey 是本地时区日键（预算是"用户的一天"）。
func todayKey() string {
	return time.Now().Format("2006-01-02")
}

// setPremiumBudgetHeader 给裸 @premium（routeSrc=premium_auto）请求盖
// 预算状态头：lane=paid（付费优先序生效中）或 lane=free（今日预算已
// 耗尽，策略已反转为免费优先）。其他路由/策略不打扰。
func (s *Server) setPremiumBudgetHeader(w http.ResponseWriter, routeSrc routing.ReasonCode) {
	if s == nil || s.router == nil || s.router.DailyBudgetUSD <= 0 || string(routeSrc) != "premium_auto" {
		return
	}
	spent := 0.0
	if s.costLedger != nil {
		spent = s.costLedger.TodayTotal()
	}
	lane := "paid"
	if spent >= s.router.DailyBudgetUSD {
		lane = "free"
	}
	w.Header().Set("X-OmniFusion-Budget",
		"lane="+lane+" spent="+formatCostUSD(spent)+" budget="+formatCostUSD(s.router.DailyBudgetUSD))
}

// formatCostUSD 把美元估算格式化为稳定短串（成本头与 usage 页共用）。
func formatCostUSD(usd float64) string {
	switch {
	case usd == 0:
		return "0"
	case usd < 0.01:
		return strconv.FormatFloat(usd, 'f', 6, 64)
	case usd < 1:
		return strconv.FormatFloat(usd, 'f', 4, 64)
	default:
		return strconv.FormatFloat(usd, 'f', 2, 64)
	}
}
