package store

import (
	"database/sql"
	"fmt"
)

// Connection 是一条 BYOK 密钥记录（密文）。
type Connection struct {
	Provider  string
	KeyCipher []byte
	Label     string
	UpdatedAt string
}

// SetConnection 写入或覆盖一条密钥记录（updated_at 刷新）。
func (s *Store) SetConnection(provider string, keyCipher []byte, label string) error {
	_, err := s.db.Exec(
		`INSERT INTO connections (provider, key_cipher, label, updated_at)
		 VALUES (?, ?, ?, datetime('now'))
		 ON CONFLICT(provider) DO UPDATE SET
			key_cipher = excluded.key_cipher,
			label      = excluded.label,
			updated_at = excluded.updated_at`,
		provider, keyCipher, label)
	if err != nil {
		return fmt.Errorf("set connection %q: %w", provider, err)
	}
	return nil
}

// GetConnection 读取密文；不存在时返回 (nil, nil)。
func (s *Store) GetConnection(provider string) (*Connection, error) {
	var c Connection
	err := s.db.QueryRow(
		`SELECT provider, key_cipher, label, updated_at FROM connections WHERE provider = ?`,
		provider,
	).Scan(&c.Provider, &c.KeyCipher, &c.Label, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get connection %q: %w", provider, err)
	}
	return &c, nil
}

// ListConnections 返回全部记录（provider 序），只含元数据与密文。
func (s *Store) ListConnections() ([]Connection, error) {
	rows, err := s.db.Query(
		`SELECT provider, key_cipher, label, updated_at FROM connections ORDER BY provider`)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Connection
	for rows.Next() {
		var c Connection
		if err := rows.Scan(&c.Provider, &c.KeyCipher, &c.Label, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan connection: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteConnection 删除一条记录；目标不存在不算错误。
func (s *Store) DeleteConnection(provider string) error {
	_, err := s.db.Exec(`DELETE FROM connections WHERE provider = ?`, provider)
	if err != nil {
		return fmt.Errorf("delete connection %q: %w", provider, err)
	}
	return nil
}
