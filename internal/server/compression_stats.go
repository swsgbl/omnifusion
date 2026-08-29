// compression_stats.go 是 M5.6 压缩统计面：comboCompress 单点聚合每组合
// 与每阶段的运行计数（runs/applied/skipped/gate_rejected/errors 与
// token 前后量），经 /dashboard/api/compression/stats（ScopeCompression）
// 供 compression 页展示。内存态——与 scorer/quota 同生命周期，重启清零
// （口径写进 API 响应，页面 meta 注明）。
package server

import (
	"net/http"
	"sort"
	"sync"

	"github.com/swsgbl/omnifusion/internal/compression"
)

// stageAgg 是单阶段的累计行。
type stageAgg struct {
	Runs         int64 `json:"runs"`
	Applied      int64 `json:"applied"`
	Skipped      int64 `json:"skipped"`
	GateRejected int64 `json:"gate_rejected"`
	Errors       int64 `json:"errors"`
	TokensBefore int64 `json:"tokens_before"`
	TokensAfter  int64 `json:"tokens_after"`
}

// comboAgg 是单组合的累计行（含纯路由组合——不压缩也计 runs）。
type comboAgg struct {
	Runs         int64 `json:"runs"`
	TokensBefore int64 `json:"tokens_before"`
	TokensAfter  int64 `json:"tokens_after"`
}

// stageRow / comboRow 是带名字的展示行（snapshot 排序产物）。
type stageRow struct {
	Stage string   `json:"stage"`
	Stats stageAgg `json:"stats"`
}
type comboStatRow struct {
	Combo string   `json:"combo"`
	Stats comboAgg `json:"stats"`
}

// comboStats 是并发安全的聚合器；零值可用。
type comboStats struct {
	mu     sync.Mutex
	combos map[string]*comboAgg
	stages map[string]*stageAgg
}

// record 记一次组合压缩运行（before/after 是组合整体 token 前后量）。
func (c *comboStats) record(combo string, before, after int64, stats []compression.CompressionStats) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.combos == nil {
		c.combos = map[string]*comboAgg{}
		c.stages = map[string]*stageAgg{}
	}
	cb := c.combos[combo]
	if cb == nil {
		cb = &comboAgg{}
		c.combos[combo] = cb
	}
	cb.Runs++
	cb.TokensBefore += before
	cb.TokensAfter += after
	for _, st := range stats {
		sa := c.stages[st.Stage]
		if sa == nil {
			sa = &stageAgg{}
			c.stages[st.Stage] = sa
		}
		sa.Runs++
		sa.TokensBefore += int64(st.BeforeTokens)
		sa.TokensAfter += int64(st.AfterTokens)
		switch {
		case st.Err != nil:
			sa.Errors++
		case st.GateRejected != nil:
			sa.GateRejected++
		case st.Skipped:
			sa.Skipped++
		case st.Applied:
			sa.Applied++
		}
	}
}

// snapshot 返回按名字排序的展示行（副本，调用方可自由持有）。
func (c *comboStats) snapshot() ([]stageRow, []comboStatRow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stages := make([]stageRow, 0, len(c.stages))
	for name, sa := range c.stages {
		stages = append(stages, stageRow{Stage: name, Stats: *sa})
	}
	sort.Slice(stages, func(i, j int) bool { return stages[i].Stage < stages[j].Stage })
	combos := make([]comboStatRow, 0, len(c.combos))
	for name, cb := range c.combos {
		combos = append(combos, comboStatRow{Combo: name, Stats: *cb})
	}
	sort.Slice(combos, func(i, j int) bool { return combos[i].Combo < combos[j].Combo })
	return stages, combos
}

// handleCompressionStats 输出压缩统计（ScopeCompression；未装配组合时空
// 表——口径稳定）。
func (s *Server) handleCompressionStats(w http.ResponseWriter, _ *http.Request) {
	stages, combos := s.cstats.snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"stages":        stages,
		"combos":        combos,
		"default_combo": s.defaultCombo(),
		"scope_note":    "in-memory counters; reset on gateway restart",
	})
}
