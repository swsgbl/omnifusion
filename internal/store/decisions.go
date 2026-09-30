// decisions.go 是路由决策回放记录的 SQLite 持久化（蓝图 Phase 9
// replay record）：每次分发请求一行，evidence 列存 RouteDecision
// 全量 JSON（候选集+逐候选结果+原因码+耗时——能解释决策的最小
// 证据集；构造性不含用户载荷/prompt/响应文本）。只追加不修改，
// 查询面 LoadRecentRouteDecisions 供排障/未来 dashboard。
package store

import (
	"fmt"
)

// RouteDecisionRow 是 route_decisions 表的一行。
type RouteDecisionRow struct {
	ID             int64
	TS             int64
	Endpoint       string
	RequestID      string
	Model          string
	ChosenProvider string
	Success        bool
	Tries          int
	DurationMS     int64
	Evidence       string // RouteDecision JSON 全量
}

// InsertRouteDecision 追加一条决策证据（失败上抛——调用方降级为
// warn 日志，永不阻断请求路径）。
func (s *Store) InsertRouteDecision(r RouteDecisionRow) error {
	_, err := s.db.Exec(`
		INSERT INTO route_decisions (
			ts, endpoint, request_id, model, chosen_provider,
			success, tries, duration_ms, evidence
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TS, r.Endpoint, r.RequestID, r.Model, r.ChosenProvider,
		boolToInt(r.Success), r.Tries, r.DurationMS, r.Evidence)
	if err != nil {
		return fmt.Errorf("insert route decision: %w", err)
	}
	return nil
}

// LoadRecentRouteDecisions 返回最近 limit 条（ts 倒序；limit<=0 用
// 默认 50）。
func (s *Store) LoadRecentRouteDecisions(limit int) ([]RouteDecisionRow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT id, ts, endpoint, request_id, model, chosen_provider,
		       success, tries, duration_ms, evidence
		FROM route_decisions ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("load route decisions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []RouteDecisionRow
	for rows.Next() {
		var r RouteDecisionRow
		var ok int
		if err := rows.Scan(
			&r.ID, &r.TS, &r.Endpoint, &r.RequestID, &r.Model, &r.ChosenProvider,
			&ok, &r.Tries, &r.DurationMS, &r.Evidence,
		); err != nil {
			return nil, fmt.Errorf("scan route decision row: %w", err)
		}
		r.Success = ok != 0
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate route decisions: %w", err)
	}
	return out, nil
}
