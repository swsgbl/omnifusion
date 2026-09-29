// entitlements.go 是权益账本的 SQLite 持久化（蓝图 Phase 3 持久化切片）：
// (provider, model) 主键的 Entitlement 快照，upsert 单条 + 全表恢复。
// 账本对象（internal/quota.Entitlement）与行对象经本文件往返——
// 时间字段 RFC3339、remaining NULL↔-1（SQL 表未知，Go 表哨兵）。
package store

import (
	"fmt"
	"time"

	"github.com/swsgbl/omnifusion/internal/quota"
)

// EntitlementRow 是 entitlements 表的一行（与 quota.Entitlement 同构，
// 时间字段以 RFC3339 往返）。
type EntitlementRow struct {
	Provider   string
	Model      string
	State      string
	Source     string
	EvidenceID string
	TermsURL   string
	RPM        int64
	TPM        int64
	RPD        int64
	TPD        int64
	// Remaining 是余量比例 [0,1]；nil = SQL NULL = 未知（Go 侧 -1 哨兵）。
	Remaining      *float64
	ObservedAt     string
	ValidUntil     string
	Confidence     float64
	CatalogVersion string
}

// UpsertEntitlement 单条 upsert（主键 (provider, model) 冲突即整行替换：
// 账本的 Record 合并已在上层完成，这里只落最终现状）。nil remaining 落 NULL。
func (s *Store) UpsertEntitlement(r EntitlementRow) error {
	if r.Provider == "" {
		return fmt.Errorf("upsert entitlement: empty provider")
	}
	_, err := s.db.Exec(`
		INSERT INTO entitlements (
			provider, model, state, source, evidence_id, terms_url,
			rpm, tpm, rpd, tpd, remaining,
			observed_at, valid_until, confidence, catalog_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (provider, model) DO UPDATE SET
			state = excluded.state,
			source = excluded.source,
			evidence_id = excluded.evidence_id,
			terms_url = excluded.terms_url,
			rpm = excluded.rpm, tpm = excluded.tpm,
			rpd = excluded.rpd, tpd = excluded.tpd,
			remaining = excluded.remaining,
			observed_at = excluded.observed_at,
			valid_until = excluded.valid_until,
			confidence = excluded.confidence,
			catalog_version = excluded.catalog_version,
			updated_at = datetime('now')`,
		r.Provider, r.Model, r.State, r.Source, r.EvidenceID, r.TermsURL,
		r.RPM, r.TPM, r.RPD, r.TPD, r.Remaining,
		r.ObservedAt, r.ValidUntil, r.Confidence, r.CatalogVersion)
	if err != nil {
		return fmt.Errorf("upsert entitlement %q/%q: %w", r.Provider, r.Model, err)
	}
	return nil
}

// LoadEntitlements 返回全表行（启动恢复用），按 (provider, model) 排序。
func (s *Store) LoadEntitlements() ([]EntitlementRow, error) {
	rows, err := s.db.Query(`
		SELECT provider, model, state, source, evidence_id, terms_url,
		       rpm, tpm, rpd, tpd, remaining,
		       observed_at, valid_until, confidence, catalog_version
		FROM entitlements ORDER BY provider, model`)
	if err != nil {
		return nil, fmt.Errorf("load entitlements: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []EntitlementRow
	for rows.Next() {
		var r EntitlementRow
		if err := rows.Scan(
			&r.Provider, &r.Model, &r.State, &r.Source, &r.EvidenceID, &r.TermsURL,
			&r.RPM, &r.TPM, &r.RPD, &r.TPD, &r.Remaining,
			&r.ObservedAt, &r.ValidUntil, &r.Confidence, &r.CatalogVersion,
		); err != nil {
			return nil, fmt.Errorf("scan entitlement row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate entitlements: %w", err)
	}
	return out, nil
}

// SaveEntitlement 把账本对象转为行并 upsert（便捷入口）。
func (s *Store) SaveEntitlement(e quota.Entitlement) error {
	row := EntitlementRow{
		Provider: e.Provider, Model: e.Model,
		State: string(e.State), Source: string(e.Source),
		EvidenceID: e.EvidenceID, TermsURL: e.TermsURL,
		RPM: int64(e.Window.RPM), TPM: e.Window.TPM,
		RPD: int64(e.Window.RPD), TPD: e.Window.TPD,
		ObservedAt: formatRFC3339(e.ObservedAt),
		ValidUntil: formatRFC3339(e.ValidUntil),
		Confidence: e.Confidence, CatalogVersion: e.CatalogVersion,
	}
	// -1 哨兵（未知）→ SQL NULL；[0,1] 照存。
	if e.Window.Remaining >= 0 {
		rem := e.Window.Remaining
		row.Remaining = &rem
	}
	return s.UpsertEntitlement(row)
}

// RestoreEntitlements 把全表行转回账本对象（启动恢复入口）。
// 过期降级不在恢复时做——Get 时自动降级并写回（quota.Ledger 语义）。
func (s *Store) RestoreEntitlements() ([]quota.Entitlement, error) {
	rows, err := s.LoadEntitlements()
	if err != nil {
		return nil, err
	}
	out := make([]quota.Entitlement, 0, len(rows))
	for _, r := range rows {
		e := quota.Entitlement{
			Provider: r.Provider, Model: r.Model,
			State:      quota.EntitlementState(r.State),
			Source:     quota.Source(r.Source),
			EvidenceID: r.EvidenceID, TermsURL: r.TermsURL,
			Window: quota.QuotaWindow{
				RPM: int(r.RPM), TPM: r.TPM, RPD: int(r.RPD), TPD: r.TPD,
			},
			Confidence: r.Confidence, CatalogVersion: r.CatalogVersion,
			ObservedAt: parseRFC3339(r.ObservedAt),
			ValidUntil: parseRFC3339(r.ValidUntil),
		}
		if r.Remaining != nil {
			e.Window.Remaining = *r.Remaining
		} else {
			e.Window.Remaining = -1
		}
		out = append(out, e)
	}
	return out, nil
}

func formatRFC3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func parseRFC3339(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
