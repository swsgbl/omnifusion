// requestlog.go 是 M5.5 请求审计日志的持久化：docs/04 §6 request_log 表
// （落地产出列——strategy 是路由内部状态，审计以入站端点+赢家口径）。
// 只增不改：每次数据面请求一行（含护栏拦截的 400），查询按 ts 倒序，
// Prune 按配置上限裁最旧行防无限增长。
package store

import (
	"fmt"
	"strings"
)

// RequestLog 是 request_log 表的一行。
type RequestLog struct {
	TS        int64  // unix 秒（请求完成时刻）
	Endpoint  string // chat|messages|gemini
	Model     string // 客户端请求的模型名（别名重写前）
	Provider  string // 赢家；缓存命中 "cache"；未分发（拦截等） ""
	Status    int    // HTTP 状态码
	TokensIn  int    // 上游 usage 口径（尽力而为）
	TokensOut int
	LatencyMS float64 // 端点入口到响应完成
	TTFTMS    float64 // 首 chunk 时延；<0 = 非流式或未知
	CacheHit  bool
	ErrKind   string // M2.1 ErrorKind / "guardrails"；成功空
	Combo     string // 命中的命名组合（可空）
}

// RequestLogFilter 是审计查询条件（零值 = 全量最新 50 行）。
type RequestLogFilter struct {
	Limit    int    // 默认 50，上限 500（查询 API 边界钳制）
	Since    int64  // unix 秒下界（含）
	Provider string // 精确匹配；空 = 不过滤
	Endpoint string // 精确匹配；空 = 过滤
}

// RequestLogID 是带行号的一行（查询输出，稳定分页序）。
type RequestLogID struct {
	ID int64
	RequestLog
}

// InsertRequestLog 落一行审计记录。
func (s *Store) InsertRequestLog(r RequestLog) error {
	_, err := s.db.Exec(
		`INSERT INTO request_log
		 (ts, endpoint, model, provider, status, tokens_in, tokens_out,
		  latency_ms, ttft_ms, cache_hit, error_kind, combo)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TS, r.Endpoint, r.Model, r.Provider, r.Status,
		r.TokensIn, r.TokensOut, r.LatencyMS, r.TTFTMS,
		boolToInt(r.CacheHit), r.ErrKind, r.Combo)
	if err != nil {
		return fmt.Errorf("insert request_log: %w", err)
	}
	return nil
}

// QueryRequestLogs 按 ts DESC（同秒按 id DESC）返回审计行。
func (s *Store) QueryRequestLogs(f RequestLogFilter) ([]RequestLogID, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 500 {
		f.Limit = 500
	}
	where := []string{}
	args := []any{}
	if f.Since > 0 {
		where = append(where, "ts >= ?")
		args = append(args, f.Since)
	}
	if f.Provider != "" {
		where = append(where, "provider = ?")
		args = append(args, f.Provider)
	}
	if f.Endpoint != "" {
		where = append(where, "endpoint = ?")
		args = append(args, f.Endpoint)
	}
	q := `SELECT id, ts, endpoint, model, provider, status, tokens_in,
	      tokens_out, latency_ms, ttft_ms, cache_hit, error_kind, combo
	      FROM request_log`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY ts DESC, id DESC LIMIT ?"
	args = append(args, f.Limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query request_log: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []RequestLogID{}
	for rows.Next() {
		var r RequestLogID
		var hit int
		if err := rows.Scan(&r.ID, &r.TS, &r.Endpoint, &r.Model, &r.Provider,
			&r.Status, &r.TokensIn, &r.TokensOut, &r.LatencyMS, &r.TTFTMS,
			&hit, &r.ErrKind, &r.Combo); err != nil {
			return nil, fmt.Errorf("scan request_log: %w", err)
		}
		r.CacheHit = hit != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneRequestLogs 只保留最新 keep 行，返回裁掉行数（keep<=0 不动）。
func (s *Store) PruneRequestLogs(keep int) (int64, error) {
	if keep <= 0 {
		return 0, nil
	}
	res, err := s.db.Exec(
		`DELETE FROM request_log WHERE id <=
		 (SELECT id FROM request_log ORDER BY id DESC LIMIT 1 OFFSET ?)`,
		keep)
	if err != nil {
		return 0, fmt.Errorf("prune request_log: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// CountRequestLogs 返回总行数（Dashboard/测试面）。
func (s *Store) CountRequestLogs() (int64, error) {
	var n int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM request_log`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count request_log: %w", err)
	}
	return n, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
