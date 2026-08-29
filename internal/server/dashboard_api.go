// dashboard_api.go 是 Dashboard v0 的三个 JSON 端点（M4.8）：providers
// 聚合 router/catalog/scorer/store 隔离态；keys 合并 cmd 注入的 key 来源
// 与 connections 表；usage 读 QuotaTracker 滑窗快照与语义缓存计数。
// 各依赖未装配时按空态返回（端点形状稳定，页面不至于拿到 5xx）。
package server

import (
	"net/http"
	"sort"
	"time"

	"github.com/swsgbl/omnifusion/internal/store"
)

// dashCooldown 是 providers 页里的一条活跃隔离。
type dashCooldown struct {
	Scope  string `json:"scope"`
	Model  string `json:"model,omitempty"`
	Until  string `json:"until"`
	Reason string `json:"reason"`
}

// dashProvider 是 providers 页的一行。
type dashProvider struct {
	Name          string         `json:"name"`
	Models        int            `json:"models"`
	LatencyMS     float64        `json:"latency_ms"`
	SuccessRate   float64        `json:"success_rate"`
	LastSuccessAt *string        `json:"last_success_at"`
	Cooldowns     []dashCooldown `json:"cooldowns"`
}

// handleDashboardProviders 返回已装配 provider 的健康视图。
func (s *Server) handleDashboardProviders(w http.ResponseWriter, _ *http.Request) {
	models := map[string]int{}
	total := 0
	if s.catalog != nil {
		for _, e := range s.catalog.Snapshot() {
			models[e.Provider]++
			total++
		}
	}
	cds := s.activeCooldowns()

	out := struct {
		Providers   []dashProvider `json:"providers"`
		ModelsTotal int            `json:"models_total"`
	}{Providers: []dashProvider{}}
	if s.router != nil {
		for _, p := range s.router.Providers {
			dp := dashProvider{
				Name: p.Name(), Models: models[p.Name()],
				Cooldowns: append([]dashCooldown{}, cds[p.Name()]...),
			}
			if s.router.Scoring != nil {
				dp.LatencyMS, dp.SuccessRate = s.router.Scoring.Snapshot(p.Name())
				if ts, ok := s.router.Scoring.LastSuccessAt(p.Name()); ok {
					str := ts.UTC().Format(time.RFC3339)
					dp.LastSuccessAt = &str
				}
			}
			out.Providers = append(out.Providers, dp)
		}
	}
	out.ModelsTotal = total
	writeJSON(w, http.StatusOK, out)
}

// activeCooldowns 从 store 读活跃隔离并按 provider 分组（读失败按空处理）。
func (s *Server) activeCooldowns() map[string][]dashCooldown {
	out := map[string][]dashCooldown{}
	if s.st == nil {
		return out
	}
	cds, err := s.st.LoadCooldowns(time.Now())
	if err != nil {
		if s.log != nil {
			s.log.Warn("dashboard: load cooldowns", "err", err)
		}
		return out
	}
	for _, c := range cds {
		out[c.Provider] = append(out[c.Provider], dashCooldown{
			Scope: c.ScopeType, Model: c.Model,
			Until: c.Until.UTC().Format(time.RFC3339), Reason: c.Reason,
		})
	}
	return out
}

// dashKey 是 keys 页的一行；Source 为 stored / env:VAR / none / -。
type dashKey struct {
	Provider  string `json:"provider"`
	Source    string `json:"source"`
	Label     string `json:"label,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// handleDashboardKeys 合并注入的 key 来源（cmd/ofd 装配期事实）与
// connections 表（stored 记录的 label/updated_at；密文永不离开 store）。
func (s *Server) handleDashboardKeys(w http.ResponseWriter, _ *http.Request) {
	keys := map[string]dashKey{}
	for p, src := range s.keySources {
		keys[p] = dashKey{Provider: p, Source: src}
	}
	if s.st != nil {
		if conns, err := s.st.ListConnections(); err == nil {
			mergeStoredKeys(keys, conns)
		} else if s.log != nil {
			s.log.Warn("dashboard: list connections", "err", err)
		}
	}
	out := make([]dashKey, 0, len(keys))
	for _, k := range keys {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

// mergeStoredKeys 用 connections 表覆盖 stored 记录（保留注入来源之外
// 的 label 与 updated_at）。
func mergeStoredKeys(keys map[string]dashKey, conns []store.Connection) {
	for _, c := range conns {
		if len(c.KeyCipher) == 0 {
			continue
		}
		k := keys[c.Provider]
		k.Provider, k.Source, k.Label, k.UpdatedAt = c.Provider, "stored", c.Label, c.UpdatedAt
		keys[c.Provider] = k
	}
}

// dashLimits 是 usage 页的配额声明（0 = 未设限）。
type dashLimits struct {
	RPM int   `json:"rpm"`
	RPD int   `json:"rpd"`
	TPM int64 `json:"tpm"`
	TPD int64 `json:"tpd"`
}

// dashUsage 是 usage 页的一行。
type dashUsage struct {
	Provider string     `json:"provider"`
	RPM      int        `json:"rpm"`
	RPD      int        `json:"rpd"`
	TPM      int64      `json:"tpm"`
	TPD      int64      `json:"tpd"`
	Limits   dashLimits `json:"limits"`
	Headroom float64    `json:"headroom"`
}

// handleDashboardUsage 返回各 key 的四窗口滑窗用量与语义缓存计数。
func (s *Server) handleDashboardUsage(w http.ResponseWriter, _ *http.Request) {
	out := struct {
		Usage        []dashUsage `json:"usage"`
		CacheEntries int64       `json:"cache_entries"`
	}{Usage: []dashUsage{}}
	if s.router != nil && s.router.Quota != nil {
		snaps := s.router.Quota.Snapshots()
		names := make([]string, 0, len(snaps))
		for n := range snaps {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			sn := snaps[n]
			out.Usage = append(out.Usage, dashUsage{
				Provider: n, RPM: sn.RPM, RPD: sn.RPD, TPM: sn.TPM, TPD: sn.TPD,
				Limits:   dashLimits{RPM: sn.Limits.RPM, RPD: sn.Limits.RPD, TPM: sn.Limits.TPM, TPD: sn.Limits.TPD},
				Headroom: sn.Headroom,
			})
		}
	}
	if s.st != nil {
		if n, err := s.st.CountSemanticCache(); err == nil {
			out.CacheEntries = n
		} else if s.log != nil {
			s.log.Warn("dashboard: count semantic cache", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
