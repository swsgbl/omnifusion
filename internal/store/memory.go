package store

import (
	"fmt"
)

// MemoryRow 是 session_memory 命中的一行（原文列；被索引的 tokens
// 仅服务检索，不回传）。
type MemoryRow struct {
	SessionID   string
	Role        string
	Content     string
	CreatedUnix int64
}

// InsertSessionMemory 插入一行会话记忆。tokens 是调用方按
// 「拉丁小写词 + CJK bigram」预处理的空格串（见 intelligence 包），
// content 存原文（UNINDEXED，注入时直接取用）。
func (s *Store) InsertSessionMemory(sessionID, role, content, tokens string, createdUnix int64) error {
	_, err := s.db.Exec(
		`INSERT INTO session_memory (session_id, role, content, tokens, created_unix)
		 VALUES (?, ?, ?, ?, ?)`,
		sessionID, role, content, tokens, createdUnix)
	if err != nil {
		return fmt.Errorf("insert session_memory: %w", err)
	}
	return nil
}

// SearchSessionMemory 按 FTS5 MATCH 表达式检索，bm25 相关性排序
// （rank 升序 = 越相关越前），返回至多 limit 行。matchExpr 由调用方
// 用白名单 token 双引号包裹构造，防用户输入被解析为 FTS5 语法。
func (s *Store) SearchSessionMemory(matchExpr string, limit int) ([]MemoryRow, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := s.db.Query(
		`SELECT session_id, role, content, created_unix
		 FROM session_memory WHERE session_memory MATCH ?
		 ORDER BY rank LIMIT ?`, matchExpr, limit)
	if err != nil {
		return nil, fmt.Errorf("search session_memory: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []MemoryRow{}
	for rows.Next() {
		var m MemoryRow
		if err := rows.Scan(&m.SessionID, &m.Role, &m.Content, &m.CreatedUnix); err != nil {
			return nil, fmt.Errorf("scan session_memory: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session_memory: %w", err)
	}
	return out, nil
}

// CountSessionMemory 返回会话记忆行数（容量检查）。
func (s *Store) CountSessionMemory() (int64, error) {
	var n int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM session_memory`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count session_memory: %w", err)
	}
	return n, nil
}

// TrimSessionMemory 按 rowid 保留最新 maxRows 行（FTS5 表 rowid 随
// 插入递增即写入序），返回删除行数。
func (s *Store) TrimSessionMemory(maxRows int) (int64, error) {
	res, err := s.db.Exec(
		`DELETE FROM session_memory WHERE rowid NOT IN (
			SELECT rowid FROM session_memory ORDER BY rowid DESC LIMIT ?
		)`, maxRows)
	if err != nil {
		return 0, fmt.Errorf("trim session_memory: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
