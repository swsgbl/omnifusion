package store

import (
	"fmt"
)

// ModelRow 是 models 表的一行（ catalog sync 的
// 持久化目标；free_meta 承载 provider 级免费层说明）。
type ModelRow struct {
	Provider string
	ID       string
	CtxLen   int64
	FreeMeta string
}

// ReplaceProviderModels 在一个事务里整组替换某 provider 的模型清单
// （删旧插新：目录同步是快照语义，不做逐条 upsert）。校验和未变化的
// 同步不会走到这里（routing.Catalog 判变更后才调用）。
func (s *Store) ReplaceProviderModels(provider, freeMeta string, rows []ModelRow) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin models replace: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM models WHERE provider = ?`, provider); err != nil {
		return fmt.Errorf("clear models for %q: %w", provider, err)
	}
	for _, r := range rows {
		if r.ID == "" {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO models (provider, id, ctx_len, free_meta) VALUES (?, ?, ?, ?)`,
			provider, r.ID, r.CtxLen, freeMeta,
		); err != nil {
			return fmt.Errorf("insert model %q/%q: %w", provider, r.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit models replace: %w", err)
	}
	return nil
}

// LoadModels 返回全表目录行（启动恢复用），按 (provider, id) 排序。
func (s *Store) LoadModels() ([]ModelRow, error) {
	rows, err := s.db.Query(
		`SELECT provider, id, ctx_len, free_meta FROM models ORDER BY provider, id`)
	if err != nil {
		return nil, fmt.Errorf("load models: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ModelRow
	for rows.Next() {
		var r ModelRow
		if err := rows.Scan(&r.Provider, &r.ID, &r.CtxLen, &r.FreeMeta); err != nil {
			return nil, fmt.Errorf("scan model row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate models: %w", err)
	}
	return out, nil
}
