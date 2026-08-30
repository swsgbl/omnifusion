package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrCacheMiss 是缓存未命中（semantic_cache 无该键或已过期）。
var ErrCacheMiss = errors.New("store: semantic cache miss")

// SemanticEntry 是 semantic_cache 命中的一行（不含 embedding_blob，
// 近似层 才启用）。
type SemanticEntry struct {
	Payload   []byte
	Timestamp int64
}

// GetSemanticCache 按 hash 取缓存行。
func (s *Store) GetSemanticCache(hash string) (SemanticEntry, error) {
	var e SemanticEntry
	err := s.db.QueryRow(
		`SELECT payload, ts FROM semantic_cache WHERE hash = ?`, hash,
	).Scan(&e.Payload, &e.Timestamp)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrCacheMiss
	}
	if err != nil {
		return e, fmt.Errorf("get semantic_cache: %w", err)
	}
	return e, nil
}

// PutSemanticCache upsert 一行（ts 取写入时刻，调用方注入以保确定性）。
func (s *Store) PutSemanticCache(hash string, payload []byte, nowUnix int64) error {
	_, err := s.db.Exec(
		`INSERT INTO semantic_cache (hash, payload, ts) VALUES (?, ?, ?)
		 ON CONFLICT(hash) DO UPDATE SET payload = excluded.payload, ts = excluded.ts`,
		hash, payload, nowUnix)
	if err != nil {
		return fmt.Errorf("put semantic_cache: %w", err)
	}
	return nil
}

// TrimSemanticCache 保留最新 maxEntries 行，其余删除（容量上限淘汰）；
// 返回删除行数。
func (s *Store) TrimSemanticCache(maxEntries int) (int64, error) {
	res, err := s.db.Exec(
		`DELETE FROM semantic_cache WHERE hash NOT IN (
			SELECT hash FROM semantic_cache ORDER BY ts DESC, hash LIMIT ?
		)`, maxEntries)
	if err != nil {
		return 0, fmt.Errorf("trim semantic_cache: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// CountSemanticCache 返回当前缓存条目数（Dashboard usage 页）。
func (s *Store) CountSemanticCache() (int64, error) {
	var n int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM semantic_cache`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count semantic_cache: %w", err)
	}
	return n, nil
}
