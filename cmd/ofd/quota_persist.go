// quota_persist.go 是账本持久化的装配适配器（依赖纪律的代价与收益）：
// store 是依赖叶子（不 import quota，depguard 门禁），quota 也是叶子
// （Persister 接口）——类型转换只能在装配层（本文件）完成。
package main

import (
	"log/slog"

	"github.com/swsgbl/omnifusion/internal/quota"
	"github.com/swsgbl/omnifusion/internal/store"
)

// storePersister 把 store 适配成 quota.Persister（Record 生效时自动落库）。
type storePersister struct {
	st  *store.Store
	log *slog.Logger
}

// SaveEntitlement 实现 quota.Persister：对象→行转换+upsert。
// 落库失败记日志不返回中断（quota.Record 已按"尽力而为"吞掉错误）。
func (p *storePersister) SaveEntitlement(e quota.Entitlement) error {
	row := store.EntitlementRow{
		Provider: e.Provider, Model: e.Model,
		State: string(e.State), Source: string(e.Source),
		EvidenceID: e.EvidenceID, TermsURL: e.TermsURL,
		RPM: int64(e.Window.RPM), TPM: e.Window.TPM,
		RPD: int64(e.Window.RPD), TPD: e.Window.TPD,
		ObservedAt: store.FormatRFC3339(e.ObservedAt),
		ValidUntil: store.FormatRFC3339(e.ValidUntil),
		Confidence: e.Confidence, CatalogVersion: e.CatalogVersion,
	}
	if e.Window.Remaining >= 0 {
		row.Remaining = &e.Window.Remaining
	} // -1 哨兵（未知）→ nil → SQL NULL
	if err := p.st.UpsertEntitlement(row); err != nil {
		if p.log != nil {
			p.log.Warn("persist entitlement failed", "provider", e.Provider, "err", err)
		}
		return err
	}
	return nil
}

// loadEntitlements 从库恢复账本快照（行→对象；过期降级由 Get 时做）。
func loadEntitlements(st *store.Store, log *slog.Logger) []quota.Entitlement {
	rows, err := st.LoadEntitlements()
	if err != nil {
		if log != nil {
			log.Warn("restore entitlements; starting with static seed only", "err", err)
		}
		return nil
	}
	out := make([]quota.Entitlement, 0, len(rows))
	for _, r := range rows {
		e := quota.Entitlement{
			Provider: r.Provider, Model: r.Model,
			State: quota.EntitlementState(r.State), Source: quota.Source(r.Source),
			EvidenceID: r.EvidenceID, TermsURL: r.TermsURL,
			Window: quota.QuotaWindow{
				RPM: int(r.RPM), TPM: r.TPM, RPD: int(r.RPD), TPD: r.TPD,
			},
			Confidence: r.Confidence, CatalogVersion: r.CatalogVersion,
			ObservedAt: store.ParseRFC3339(r.ObservedAt),
			ValidUntil: store.ParseRFC3339(r.ValidUntil),
		}
		if r.Remaining != nil {
			e.Window.Remaining = *r.Remaining
		} else {
			e.Window.Remaining = -1
		}
		out = append(out, e)
	}
	return out
}
