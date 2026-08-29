// audit_api.go 是 M5.5 审计查询端点：GET /dashboard/api/audit
// ?limit=&since=&provider=&endpoint=——request_log 倒序分页读取，
// scope=audit（与 health/usage 等并列的第五个作用域）。
package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/swsgbl/omnifusion/internal/store"
)

// auditRow 是审计 API 的一行（ts 统一 RFC3339 UTC）。
type auditRow struct {
	ID        int64   `json:"id"`
	TS        string  `json:"ts"`
	Endpoint  string  `json:"endpoint"`
	Model     string  `json:"model"`
	Provider  string  `json:"provider"`
	Status    int     `json:"status"`
	TokensIn  int     `json:"tokens_in"`
	TokensOut int     `json:"tokens_out"`
	LatencyMS float64 `json:"latency_ms"`
	TTFTMS    float64 `json:"ttft_ms"` // <0 = 非流式或未到达首 chunk
	CacheHit  bool    `json:"cache_hit"`
	ErrKind   string  `json:"error_kind,omitempty"`
	Combo     string  `json:"combo,omitempty"`
}

// handleDashboardAudit 读 request_log（store 未装配返回空表）。
func (s *Server) handleDashboardAudit(w http.ResponseWriter, r *http.Request) {
	rows := []auditRow{}
	if s.st != nil {
		got, err := s.st.QueryRequestLogs(store.RequestLogFilter{
			Limit:    queryPositiveInt(r, "limit", 50),
			Since:    queryInt64(r, "since"),
			Provider: r.URL.Query().Get("provider"),
			Endpoint: r.URL.Query().Get("endpoint"),
		})
		if err != nil {
			s.log.Warn("audit: query request_log", "err", err)
			writeAPIError(w, http.StatusInternalServerError, "audit query failed", "server_error", "")
			return
		}
		for _, row := range got {
			rows = append(rows, auditRow{
				ID: row.ID, TS: time.Unix(row.TS, 0).UTC().Format(time.RFC3339),
				Endpoint: row.Endpoint, Model: row.Model, Provider: row.Provider,
				Status: row.Status, TokensIn: row.TokensIn, TokensOut: row.TokensOut,
				LatencyMS: row.LatencyMS, TTFTMS: row.TTFTMS, CacheHit: row.CacheHit,
				ErrKind: row.ErrKind, Combo: row.Combo,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": rows})
}

// queryPositiveInt 取正整数参数（缺省/非法取 def）。
func queryPositiveInt(r *http.Request, name string, def int) int {
	n, ok := queryInt(r, name)
	if !ok || n <= 0 {
		return def
	}
	return n
}

// queryInt64 取整数参数（缺省 0；非法视为缺省）。
func queryInt64(r *http.Request, name string) int64 {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// queryInt 是 queryPositiveInt 的底层（ok=false 表缺省或非法）。
func queryInt(r *http.Request, name string) (int, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	return n, err == nil
}
