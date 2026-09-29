// ledger_bridge.go 是权益账本（internal/quota）与路由层的桥（蓝图
// Phase 3 第二片）：Dispatch/DispatchStream 的失败尝试自动喂入账本，
// 把"运行时 429/402"从一次性隔离事件升级为可持续的权益证据。
//
// 喂入规则（只记直接证据）：
//   - KindQuotaExhausted + 上游 402        → RecordPaidObservation（free 层已不可用）
//   - KindQuotaExhausted + 上游 429+配额词 → RecordQuotaExhausted（窗口耗尽，free 仍在）
//   - KindRateLimit（429 无配额词）        → 不喂（限流 ≠ 配额事实）
//   - 成功尝试                              → 不喂（成功不区分免费/付费——那是定价面）
package routing

import (
	"errors"
	"net/http"
	"time"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/quota"
)

// observeLedger 把一次失败尝试的直接证据喂入账本（Ledger 未装配时
// 零行为）。在 applyIsolation 之后调用：隔离管"接下来几分钟别再试"，
// 账本管"这家的免费层现状是什么"——两套时间尺度、两套语义。
func (r *Router) observeLedger(att Attempt) {
	if r.Ledger == nil || att.Err == nil {
		return
	}
	switch att.Kind {
	case KindQuotaExhausted:
		var ue *provider.UpstreamError
		if !errors.As(att.Err, &ue) {
			return
		}
		now := time.Now()
		if ue.Status == http.StatusPaymentRequired {
			r.Ledger.RecordPaidObservation(att.Provider, att.Model, now)
		} else {
			// 429 + 配额关键词（Classify 的 quota 语义分支）
			r.Ledger.RecordQuotaExhausted(att.Provider, att.Model, now)
		}
	}
}

// EntitlementOf 是账本现状查询面（候选硬过滤/降权消费；Ledger 未装配
// 返回 UNKNOWN 零值——不确定不惩罚可用性，蓝图纪律）。
func (r *Router) EntitlementOf(providerName, model string) quota.Entitlement {
	if r.Ledger == nil {
		return quota.Entitlement{Provider: providerName, Model: model,
			State: quota.StateUnknown, Window: quota.QuotaWindow{Remaining: -1}}
	}
	return r.Ledger.Get(providerName, model)
}
