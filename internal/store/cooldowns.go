package store

import (
	"fmt"
	"time"
)

// Cooldown 是一条三层隔离状态（ cooldowns 表）。
// ScopeType "connection" 表示 Connection 冷却（provider 级）；
// "model" 表示 Model 锁定（provider+model 级，配额耗尽至重置点）。
type Cooldown struct {
	ScopeType string
	Provider  string
	Model     string // connection 层恒为空
	Until     time.Time
	Reason    string
}

// UpsertCooldown 写入一条隔离状态；同键冲突时取更晚的 until
// （短冷却不得缩短已在进行的更长冷却）。
func (s *Store) UpsertCooldown(c Cooldown) error {
	if _, err := s.db.Exec(
		`INSERT INTO cooldowns (scope_type, provider, model, until_unix, reason)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(scope_type, provider, model) DO UPDATE SET
		   until_unix = MAX(cooldowns.until_unix, excluded.until_unix),
		   reason     = excluded.reason`,
		c.ScopeType, c.Provider, c.Model, c.Until.Unix(), c.Reason,
	); err != nil {
		return fmt.Errorf("upsert cooldown %s/%s/%s: %w", c.ScopeType, c.Provider, c.Model, err)
	}
	return nil
}

// LoadCooldowns 返回 until > now 的活跃条目（启动恢复用）。
func (s *Store) LoadCooldowns(now time.Time) ([]Cooldown, error) {
	rows, err := s.db.Query(
		`SELECT scope_type, provider, model, until_unix, reason
		 FROM cooldowns WHERE until_unix > ? ORDER BY until_unix`,
		now.Unix(),
	)
	if err != nil {
		return nil, fmt.Errorf("load cooldowns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Cooldown
	for rows.Next() {
		var c Cooldown
		var until int64
		if err := rows.Scan(&c.ScopeType, &c.Provider, &c.Model, &until, &c.Reason); err != nil {
			return nil, fmt.Errorf("scan cooldown: %w", err)
		}
		c.Until = time.Unix(until, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ClearExpiredCooldowns 删除已过期条目（惰性清理，防表无限膨胀）。
func (s *Store) ClearExpiredCooldowns(now time.Time) error {
	if _, err := s.db.Exec(
		`DELETE FROM cooldowns WHERE until_unix <= ?`, now.Unix(),
	); err != nil {
		return fmt.Errorf("clear expired cooldowns: %w", err)
	}
	return nil
}

// ClearCooldowns 删除一个 provider 的全部隔离条目（运维清除：
// MCP route 工具经控制 API 到 Isolation.Clear）。返回删除行数。
func (s *Store) ClearCooldowns(provider string) (int64, error) {
	res, err := s.db.Exec(
		`DELETE FROM cooldowns WHERE provider = ?`, provider,
	)
	if err != nil {
		return 0, fmt.Errorf("clear cooldowns %s: %w", provider, err)
	}
	return res.RowsAffected()
}
