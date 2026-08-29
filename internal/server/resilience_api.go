// resilience_api.go 是 M5.6 弹性状态可视化面：GET /dashboard/api/resilience
// （ScopeRoute）一屏聚合钉选（M5.2）、三层隔离（store 持久化的冷却/锁定
// 含 reason）、熔断器（仅内存，routing.Breakers()）、provider 健康信号
// （scorer EWMA）与近期失败请求（M5.5 request_log 审计行按错误类分组）。
// 各依赖未装配时按空态返回（形状稳定）。
package server

import (
	"net/http"
	"sort"
	"time"

	"github.com/swsgbl/omnifusion/internal/store"
)

// resiProvider 是 providers 段的一行。
type resiProvider struct {
	Name          string  `json:"name"`
	LatencyMS     float64 `json:"latency_ms"`
	SuccessRate   float64 `json:"success_rate"`
	LastSuccessAt *string `json:"last_success_at"`
	Cooldowns     int     `json:"cooldowns"`
	Isolated      bool    `json:"isolated"` // 连接层冷却或熔断开
}

// resiCooldown 是 cooldowns 段的一行（store 权威，含 reason）。
type resiCooldown struct {
	Provider string `json:"provider"`
	Scope    string `json:"scope"` // connection / model
	Model    string `json:"model,omitempty"`
	Until    string `json:"until"`
	Reason   string `json:"reason"`
}

// resiBreaker 是 breakers 段的一行（仅内存，重启清零）。
type resiBreaker struct {
	Provider string  `json:"provider"`
	State    string  `json:"state"` // closed / open / half-open
	Failures int     `json:"failures"`
	OpenTill *string `json:"open_till,omitempty"`
}

// resiFailureKind 是 failures 段按 error_kind 分组的计数行。
type resiFailureKind struct {
	Kind     string `json:"kind"`
	Count    int    `json:"count"`
	LastSeen string `json:"last_seen"`
}

// resiFailureRow 是 failures 明细的最新 N 行。
type resiFailureRow struct {
	TS        string `json:"ts"`
	Status    int    `json:"status"`
	Endpoint  string `json:"endpoint"`
	Model     string `json:"model"`
	Provider  string `json:"provider"`
	ErrorKind string `json:"error_kind"`
}

// handleResilience 输出弹性状态聚合。
func (s *Server) handleResilience(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.resilienceJSON())
}

// resilienceJSON 组装响应体（独立函数便于测试直接断言）。
func (s *Server) resilienceJSON() map[string]any {
	out := s.pinStatusJSON()

	byProvider := map[string]int{} // provider → 活跃隔离条数
	connCooling := map[string]bool{}
	cooldowns := []resiCooldown{}
	for p, list := range s.activeCooldowns() {
		byProvider[p] = len(list)
		for _, c := range list {
			if c.Scope == "connection" {
				connCooling[p] = true
			}
			cooldowns = append(cooldowns, resiCooldown{Provider: p, Scope: c.Scope,
				Model: c.Model, Until: c.Until, Reason: c.Reason})
		}
	}
	sort.Slice(cooldowns, func(i, j int) bool { return cooldowns[i].Until < cooldowns[j].Until })
	out["cooldowns"] = cooldowns

	breakerOpen := map[string]bool{}
	breakers := []resiBreaker{}
	if s.router != nil && s.router.Isolation != nil {
		for _, b := range s.router.Isolation.Breakers() {
			row := resiBreaker{Provider: b.Provider, State: b.State, Failures: b.Failures}
			if b.OpenTill != nil {
				str := b.OpenTill.UTC().Format(time.RFC3339)
				row.OpenTill = &str
			}
			if b.State != "closed" {
				breakerOpen[b.Provider] = true
			}
			breakers = append(breakers, row)
		}
	}
	out["breakers"] = breakers

	providers := []resiProvider{}
	if s.router != nil {
		for _, p := range s.router.Providers {
			rp := resiProvider{Name: p.Name(), Cooldowns: byProvider[p.Name()],
				Isolated: connCooling[p.Name()] || breakerOpen[p.Name()]}
			if s.router.Scoring != nil {
				rp.LatencyMS, rp.SuccessRate = s.router.Scoring.Snapshot(p.Name())
				if ts, ok := s.router.Scoring.LastSuccessAt(p.Name()); ok {
					str := ts.UTC().Format(time.RFC3339)
					rp.LastSuccessAt = &str
				}
			}
			providers = append(providers, rp)
		}
	}
	out["providers"] = providers

	kinds, rows := s.recentFailures()
	out["failure_kinds"] = kinds
	out["failure_rows"] = rows
	return out
}

// recentFailures 从 request_log 聚合失败视图：近 200 行里 status>=400 的
// 按 error_kind 分组计数 + 最新 10 行明细（查询 ts 倒序，天然最新在前）。
func (s *Server) recentFailures() ([]resiFailureKind, []resiFailureRow) {
	kinds := []resiFailureKind{}
	rows := []resiFailureRow{}
	if s.st == nil || !s.auditEnabled() {
		return kinds, rows
	}
	logs, err := s.st.QueryRequestLogs(store.RequestLogFilter{Limit: 200})
	if err != nil {
		if s.log != nil {
			s.log.Warn("resilience: query request logs", "err", err)
		}
		return kinds, rows
	}
	byKind := map[string]*resiFailureKind{}
	for _, l := range logs {
		if l.Status < 400 {
			continue
		}
		ts := time.Unix(l.TS, 0).UTC().Format(time.RFC3339)
		if k := byKind[l.ErrKind]; k != nil {
			k.Count++
			k.LastSeen = ts
		} else {
			byKind[l.ErrKind] = &resiFailureKind{Kind: l.ErrKind, Count: 1, LastSeen: ts}
		}
		if len(rows) < 10 {
			rows = append(rows, resiFailureRow{TS: ts, Status: l.Status,
				Endpoint: l.Endpoint, Model: l.Model, Provider: l.Provider,
				ErrorKind: l.ErrKind})
		}
	}
	for _, k := range byKind {
		kinds = append(kinds, *k)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i].Count > kinds[j].Count })
	return kinds, rows
}
