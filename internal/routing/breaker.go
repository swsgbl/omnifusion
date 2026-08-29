// breaker.go 是三层隔离的第三层：provider 级熔断器
// （docs/04 §4.4：closed → open(错误率阈值) → half-open(探测) → closed）。
// 只计入"上游病了"类错误（5xx/net/stream_broken）；限流/配额/鉴权是
// 容量与凭据语义，由冷却/锁定层处理，不进熔断窗口。
package routing

import "time"

type breakerState int8

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

const (
	// breakerWindow 是观测窗口：最近 N 次结果。
	breakerWindow = 20
	// breakerFailThresh 是窗口内触发熔断的最少失败次数。
	breakerFailThresh = 5
	// breakerFailRate 是触发熔断的失败率下限。
	breakerFailRate = 0.5
	// breakerOpenBase / breakerOpenMax 是熔断退避的基准与上限
	//（连续熔断指数翻倍：30s,60s,120s…封顶 10m）。
	breakerOpenBase = 30 * time.Second
	breakerOpenMax  = 10 * time.Minute
)

// breaker 记录一个 provider 的熔断状态。生命周期短（30s 级退避），
// 仅内存持有，重启即重置；冷却/锁定才持久化（FreeRide 教训针对的是
// 长冷却，熔断状态自愈极快）。
type breaker struct {
	state    breakerState
	outcomes []bool // 最近窗口，true=失败；容量 ≤ breakerWindow
	openSeq  int    // 连续熔断次数（退避指数）
	openTill time.Time
}

// blocked 报告当前是否隔离。open 且到点时原子转入 half-open 并放行
// 一次探测；探测在途期间（half-open）其余请求照旧隔离，从而保证
// 单探测语义。
func (b *breaker) blocked(now time.Time) (bool, time.Duration) {
	switch b.state {
	case breakerOpen:
		if now.Before(b.openTill) {
			return true, b.openTill.Sub(now)
		}
		b.state = breakerHalfOpen // 放行一次探测
		return false, 0
	case breakerHalfOpen:
		return true, 0 // 探测已在途
	default:
		return false, 0
	}
}

// onFailure 记一次失败；half-open 探测失败立即重熔断（退避翻倍），
// closed 且达阈值时熔断。
func (b *breaker) onFailure(now time.Time) {
	b.record(true) // true = 失败
	switch b.state {
	case breakerHalfOpen:
		b.trip(now)
	case breakerClosed:
		if b.shouldTrip() {
			b.trip(now)
		}
	}
}

// onSuccess 记一次成功；half-open 探测成功即闭合并清零退避。
func (b *breaker) onSuccess() {
	b.record(false) // false = 成功
	if b.state == breakerHalfOpen {
		b.state = breakerClosed
		b.openSeq = 0
		b.outcomes = b.outcomes[:0] // 恢复后从新窗口起步
	}
}

func (b *breaker) record(failed bool) {
	b.outcomes = append(b.outcomes, failed)
	if len(b.outcomes) > breakerWindow {
		// 滑出窗口（append 头部；窗口小，拷贝可忽略）
		b.outcomes = b.outcomes[len(b.outcomes)-breakerWindow:]
	}
}

func (b *breaker) shouldTrip() bool {
	n := len(b.outcomes)
	if n < breakerFailThresh {
		return false
	}
	fails := 0
	for _, f := range b.outcomes {
		if f {
			fails++
		}
	}
	return fails >= breakerFailThresh && float64(fails)/float64(n) >= breakerFailRate
}

func (b *breaker) trip(now time.Time) {
	d := breakerOpenBase
	for i := 0; i < b.openSeq && d < breakerOpenMax; i++ {
		d *= 2
	}
	if d > breakerOpenMax {
		d = breakerOpenMax
	}
	b.openTill = now.Add(d)
	b.openSeq++
	b.state = breakerOpen
}
