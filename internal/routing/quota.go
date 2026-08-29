// quota.go 是 M2.3 per-key 配额滑动窗口（docs/04 §6 quota_counters 的
// 运行时形态）：RPM/RPD/TPM/TPD 四窗口，按事件日志实现真滑动（非固定
// 窗口对齐），到点自然滑出。预防性限流——在 429 发生之前把快爆的 key
// 沉到候选列表后面跳过；429 之后的兜底隔离由 M2.2 状态机负责。
//
// 记账纪律：RecordRequest 在成功建立响应后提交（在途请求不计数，
// 个人网关规模下突发超限的误差可接受，openai/groq 的 429 兜底会纠偏）；
// token 用量来自响应 usage（非流式）与流尾 usage chunk（Close 时提交）。
// 窗口最长 24h，内存日志量 = 请求数/天，个人网关量级可忽略。
package routing

import (
	"fmt"
	"sync"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// quotaMinute / quotaDay 是两类窗口跨度。
const (
	quotaMinute = time.Minute
	quotaDay    = 24 * time.Hour
)

// QuotaLimits 声明一个 key 的四窗口配额；0 表示该窗口不限制。
type QuotaLimits struct {
	RPM int   // requests per minute
	RPD int   // requests per day
	TPM int64 // tokens per minute
	TPD int64 // tokens per day
}

// tokEvent 记一次 token 提交的时间与数量。
type tokEvent struct {
	ts time.Time
	n  int64
}

// keyUsage 是单 key 的滑动窗口事件日志。
type keyUsage struct {
	reqs []time.Time
	toks []tokEvent
}

// prune 滑出 24h 之前的全部事件（day 是最长窗口）。
func (u *keyUsage) prune(now time.Time) {
	cut := now.Add(-quotaDay)
	i := 0
	for ; i < len(u.reqs) && u.reqs[i].Before(cut); i++ {
	}
	u.reqs = u.reqs[i:]
	j := 0
	for ; j < len(u.toks) && u.toks[j].ts.Before(cut); j++ {
	}
	u.toks = u.toks[j:]
}

// QuotaTracker 按 key 追踪四窗口用量。零值不可用，经 NewQuotaTracker
// 构造；未 SetLimit 的 key 永不阻塞。
type QuotaTracker struct {
	mu     sync.Mutex
	limits map[string]QuotaLimits
	usage  map[string]*keyUsage
	now    func() time.Time // 时钟注入点（测试用）
}

// NewQuotaTracker 装配空配额追踪器。
func NewQuotaTracker() *QuotaTracker {
	return &QuotaTracker{
		limits: map[string]QuotaLimits{},
		usage:  map[string]*keyUsage{},
		now:    time.Now,
	}
}

// SetLimit 声明/更新一个 key 的配额（registry 免费层事实）。
func (t *QuotaTracker) SetLimit(key string, l QuotaLimits) {
	t.mu.Lock()
	t.limits[key] = l
	t.mu.Unlock()
}

// Blocked 报告该 key 再发一条请求是否会触碰任一窗口上限，并给出
// 最先命中的窗口与重置时间。token 窗口按已提交用量判断（不预估在途）。
func (t *QuotaTracker) Blocked(key string) (bool, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	l, ok := t.limits[key]
	if !ok {
		return false, ""
	}
	u := t.usage[key]
	if u == nil {
		return false, ""
	}
	now := t.now()
	u.prune(now)

	rpm := 0
	rpmFrom := -1
	for i, ts := range u.reqs {
		if now.Sub(ts) < quotaMinute {
			rpm++
			if rpmFrom < 0 {
				rpmFrom = i
			}
		}
	}
	if l.RPM > 0 && rpm >= l.RPM {
		return true, exhaustedReason("rpm", u.reqs[rpmFrom].Add(quotaMinute).Sub(now))
	}
	if l.RPD > 0 && len(u.reqs) >= l.RPD {
		return true, exhaustedReason("rpd", u.reqs[0].Add(quotaDay).Sub(now))
	}
	var tpm, tpd int64
	tpmFrom := -1
	for i, e := range u.toks {
		tpd += e.n
		if now.Sub(e.ts) < quotaMinute {
			tpm += e.n
			if tpmFrom < 0 {
				tpmFrom = i
			}
		}
	}
	if l.TPM > 0 && tpm >= l.TPM && tpmFrom >= 0 {
		return true, exhaustedReason("tpm", u.toks[tpmFrom].ts.Add(quotaMinute).Sub(now))
	}
	if l.TPD > 0 && tpd >= l.TPD {
		return true, exhaustedReason("tpd", u.toks[0].ts.Add(quotaDay).Sub(now))
	}
	return false, ""
}

// RecordRequest 提交一次成功请求（RPM/RPD 计数）。
func (t *QuotaTracker) RecordRequest(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	u := t.usageFor(key)
	u.prune(t.now())
	u.reqs = append(u.reqs, t.now())
}

// RecordTokens 提交 token 用量（TPM/TPD 计数）；n ≤ 0 忽略。
func (t *QuotaTracker) RecordTokens(key string, n int64) {
	if n <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	u := t.usageFor(key)
	u.prune(t.now())
	u.toks = append(u.toks, tokEvent{ts: t.now(), n: n})
}

// Usage 返回该 key 当前各窗口已提交用量（观测 / ofd status 用）。
func (t *QuotaTracker) Usage(key string) (rpm, rpd int, tpm, tpd int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	u := t.usage[key]
	if u == nil {
		return 0, 0, 0, 0
	}
	now := t.now()
	u.prune(now)
	rpd = len(u.reqs)
	for _, ts := range u.reqs {
		if now.Sub(ts) < quotaMinute {
			rpm++
		}
	}
	for _, e := range u.toks {
		tpd += e.n
		if now.Sub(e.ts) < quotaMinute {
			tpm += e.n
		}
	}
	return rpm, rpd, tpm, tpd
}

// QuotaSnapshot 是单 key 的配额与用量视图（Dashboard/观测用）；
// Limits 为零值表示未设限（只记用量）。
type QuotaSnapshot struct {
	Limits   QuotaLimits
	RPM, RPD int
	TPM, TPD int64
	Headroom float64
}

// Snapshots 返回全部有配额声明或有已提交用量的 key 视图（M4.8
// Dashboard usage 页的数据源）。只读、锁外组装，不阻塞记账。
func (t *QuotaTracker) Snapshots() map[string]QuotaSnapshot {
	t.mu.Lock()
	keys := make(map[string]struct{}, len(t.limits)+len(t.usage))
	for k := range t.limits {
		keys[k] = struct{}{}
	}
	for k := range t.usage {
		keys[k] = struct{}{}
	}
	limits := make(map[string]QuotaLimits, len(t.limits))
	for k, v := range t.limits {
		limits[k] = v
	}
	t.mu.Unlock()

	out := make(map[string]QuotaSnapshot, len(keys))
	for k := range keys {
		rpm, rpd, tpm, tpd := t.Usage(k)
		out[k] = QuotaSnapshot{
			Limits: limits[k], RPM: rpm, RPD: rpd, TPM: tpm, TPD: tpd,
			Headroom: t.Headroom(k),
		}
	}
	return out
}

// Headroom 返回该 key 四窗口中最紧的剩余配额比例（0=已耗尽，
// 1=余量充足或未设限），供打分路由（M2.4）把快爆的 key 沉底。
func (t *QuotaTracker) Headroom(key string) float64 {
	t.mu.Lock()
	l, limited := t.limits[key]
	t.mu.Unlock()
	if !limited {
		return 1
	}
	rpm, rpd, tpm, tpd := t.Usage(key)
	worst := 1.0
	for _, w := range []struct{ used, limit float64 }{
		{float64(rpm), float64(l.RPM)},
		{float64(rpd), float64(l.RPD)},
		{float64(tpm), float64(l.TPM)},
		{float64(tpd), float64(l.TPD)},
	} {
		if w.limit > 0 {
			if h := 1 - w.used/w.limit; h < worst {
				worst = h
			}
		}
	}
	if worst < 0 {
		worst = 0
	}
	return worst
}

func (t *QuotaTracker) usageFor(key string) *keyUsage {
	u := t.usage[key]
	if u == nil {
		u = &keyUsage{}
		t.usage[key] = u
	}
	return u
}

func exhaustedReason(window string, resetIn time.Duration) string {
	if resetIn < 0 {
		resetIn = 0
	}
	return fmt.Sprintf("quota %s exhausted (resets in %s)", window, resetIn.Round(time.Second))
}

// recordQuota 记一次成功调用的请求与 token 用量（非流式路径）。
func (r *Router) recordQuota(providerName string, resp *schema.Response) {
	if r.Quota == nil {
		return
	}
	r.Quota.RecordRequest(providerName)
	if resp != nil && resp.Usage != nil {
		r.Quota.RecordTokens(providerName, int64(resp.Usage.TotalTokens))
	}
}
