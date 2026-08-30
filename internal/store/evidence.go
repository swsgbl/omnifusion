// evidence.go 是 社区众测协议的证据面：按 (provider, model)
// 聚合 request_log 的真实流量结果，供 feed 维护者裁决众测条目升降级
// （`ofd catalog report` 消费）。
package store

import (
	"fmt"
)

// ModelEvidence 是一个模型在观测窗内的流量证据。
type ModelEvidence struct {
	Model  string
	Calls  int64
	OK     int64 // status < 400（含缓存命中）
	Errors int64 // status >= 400
}

// QueryModelEvidence 聚合 provider 自 sinceUnix 起的逐模型证据，
// 按 Calls 降序。model 为空的行（未分发请求）不计。
func (s *Store) QueryModelEvidence(provider string, sinceUnix int64) ([]ModelEvidence, error) {
	rows, err := s.db.Query(
		`SELECT model,
		        COUNT(*),
		        SUM(CASE WHEN status < 400 THEN 1 ELSE 0 END),
		        SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END)
		 FROM request_log
		 WHERE provider = ? AND ts >= ? AND model != ''
		 GROUP BY model
		 ORDER BY COUNT(*) DESC`, provider, sinceUnix)
	if err != nil {
		return nil, fmt.Errorf("query model evidence: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []ModelEvidence{}
	for rows.Next() {
		var e ModelEvidence
		if err := rows.Scan(&e.Model, &e.Calls, &e.OK, &e.Errors); err != nil {
			return nil, fmt.Errorf("scan model evidence: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model evidence: %w", err)
	}
	return out, nil
}
