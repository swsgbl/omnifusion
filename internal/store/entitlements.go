// entitlements.go 是权益账本的 SQLite 持久化（蓝图 Phase 3 持久化切片）：
// (provider, model) 主键的行级 upsert + 全表读取。本文件只处理行对象
// （EntitlementRow）——quota.Entitlement ↔ Row 的类型转换在装配层
// （cmd/ofd，quotaPersister 适配器）完成，存储层不 import quota
// （infra-store 是依赖叶子，depguard 门禁）。
package store

import (
	"fmt"
	"time"
)

// EntitlementRow 是 entitlements 表的一行（与 quota.Entitlement 同构，
// 时间字段以 RFC3339 往返）。
type EntitlementRow struct {
	Provider string
	Model    string
	State    string
	Source   string
	EvidenceID string
	TermsURL  string
	RPM      int64
	TPM      int64
	RPD      int64
	TPD      int64
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

// FormatRFC3339 / ParseRFC3339 是时间列的公共往返器（装配层适配器复用）。
func FormatRFC3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ParseRFC3339 解析 RFC3339 时间列（空串/坏串回零值）。
func ParseRFC3339(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
