// snapshot.go 是 Isolation 的只读视图（M5.6 弹性状态可视化）：冷却与
// 锁定本就持久化（store.LoadCooldowns 含 reason，dashboard 直读），唯一
// 缺的读口是仅存内存的熔断器（docs/04 §4.4：重启即重置、30s 级自愈，
// 不值得持久化）。本文件只读不推进状态——half-open 转移由 Block() 在
// 分发路径上完成，快照绝不产生副作用。
package routing

import (
	"sort"
	"time"
)

// BreakerRow 是单个 provider 熔断器的展示行。
type BreakerRow struct {
	Provider string
	State    string     // closed / open / half-open
	Failures int        // 观测窗口内失败次数（≤ breakerWindow）
	OpenTill *time.Time // state != closed 时熔断退避到期点
}

// Breakers 返回全部熔断器的当前状态（按 provider 排序）。只导出熔断器：
// 冷却/锁定的权威来源是 store（含 reason 与跨重启恢复），内存两 map 与
// 之同源（setCooldown/setLockout 均先内存后 persist，Clear 双清）。
func (iso *Isolation) Breakers() []BreakerRow {
	iso.mu.Lock()
	defer iso.mu.Unlock()
	out := make([]BreakerRow, 0, len(iso.breakers))
	for name, b := range iso.breakers {
		row := BreakerRow{Provider: name, Failures: b.windowFails()}
		switch b.state {
		case breakerOpen:
			row.State = "open"
			t := b.openTill
			row.OpenTill = &t
		case breakerHalfOpen:
			row.State = "half-open"
			t := b.openTill
			row.OpenTill = &t
		default:
			row.State = "closed"
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

// windowFails 统计观测窗口内的失败次数（调用方持锁）。
func (b *breaker) windowFails() int {
	fails := 0
	for _, f := range b.outcomes {
		if f {
			fails++
		}
	}
	return fails
}
