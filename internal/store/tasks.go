// tasks.go 是任务存储的 SQLite 持久化（蓝图 Phase 6 Tasks 扩展持久化
// 切片）：id 主键的行级 upsert + 全表读取。本文件只处理行对象
// （TaskRow）——agent.Task ↔ Row 的类型转换在装配层（cmd/ofd，
// taskPersister 适配器）完成，存储层不 import agent（infra-store 是
// 依赖叶子，depguard 门禁）。
package store

import (
	"fmt"
)

// TaskRow 是 tasks 表的一行（与 agent.Task 同构，时间字段以 RFC3339
// 往返——FormatRFC3339/ParseRFC3339 见 entitlements.go）。
type TaskRow struct {
	ID             string
	Kind           string
	Status         string
	CreatedAt      string
	UpdatedAt      string
	Deadline       string
	Result         string
	Error          string
	IdempotencyKey string
	ContextID      string
}

// UpsertTask 单条 upsert（主键 id 冲突即整行替换：状态机的迁移合法性
// 已在上层完成，这里只落最终现状）。
func (s *Store) UpsertTask(r TaskRow) error {
	if r.ID == "" {
		return fmt.Errorf("upsert task: empty id")
	}
	_, err := s.db.Exec(`
		INSERT INTO tasks (
			id, kind, status, created_at, updated_at, deadline,
			result, error, idempotency_key, context_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			kind = excluded.kind,
			status = excluded.status,
			created_at = excluded.created_at,
			updated_at = excluded.updated_at,
			deadline = excluded.deadline,
			result = excluded.result,
			error = excluded.error,
			idempotency_key = excluded.idempotency_key,
			context_id = excluded.context_id`,
		r.ID, r.Kind, r.Status, r.CreatedAt, r.UpdatedAt, r.Deadline,
		r.Result, r.Error, r.IdempotencyKey, r.ContextID)
	if err != nil {
		return fmt.Errorf("upsert task %q: %w", r.ID, err)
	}
	return nil
}

// LoadTasks 返回全表行（启动恢复用），按 id 排序。
func (s *Store) LoadTasks() ([]TaskRow, error) {
	rows, err := s.db.Query(`
		SELECT id, kind, status, created_at, updated_at, deadline,
		       result, error, idempotency_key, context_id
		FROM tasks ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []TaskRow
	for rows.Next() {
		var r TaskRow
		if err := rows.Scan(
			&r.ID, &r.Kind, &r.Status, &r.CreatedAt, &r.UpdatedAt, &r.Deadline,
			&r.Result, &r.Error, &r.IdempotencyKey, &r.ContextID,
		); err != nil {
			return nil, fmt.Errorf("scan task row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tasks: %w", err)
	}
	return out, nil
}
