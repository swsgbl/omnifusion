// dashboard_api.go 是 Dashboard v0 的三个 JSON 端点：providers
// 聚合 router/catalog/scorer/store 隔离态；keys 合并 cmd 注入的 key 来源
// 与 connections 表；usage 读 QuotaTracker 滑窗快照与语义缓存计数。
// 各依赖未装配时按空态返回（端点形状稳定，页面不至于拿到 5xx）。
package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/swsgbl/omnifusion/internal/security"
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
	// Entitlement 是权益账本现状（蓝图 Phase 3：五态+证据来源+余量；
	// Ledger 未装配时省略——不确定不吓人）。
	Entitlement *dashEntitlement `json:"entitlement,omitempty"`
}

// dashEntitlement 是 providers 页的权益视图（账本现状的可读投影）。
type dashEntitlement struct {
	State      string  `json:"state"`               // UNKNOWN/VERIFIED_FREE/VERIFIED_PAID/EXPIRED/DISABLED
	Source     string  `json:"source,omitempty"`    // 证据来源
	Remaining  float64 `json:"remaining,omitempty"` // 余量比例 [0,1]；-1=未知
	ObservedAt string  `json:"observed_at,omitempty"`
	ValidUntil string  `json:"valid_until,omitempty"`
	EvidenceID string  `json:"evidence_id,omitempty"`
	TermsURL   string  `json:"terms_url,omitempty"`
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
		Providers      []dashProvider `json:"providers"`
		ModelsTotal    int            `json:"models_total"`
		ModelsSyncedAt *string        `json:"models_synced_at,omitempty"`
	}{Providers: []dashProvider{}}
	if s.catalog != nil {
		if ts := s.catalog.LastSyncAt(); !ts.IsZero() {
			str := ts.UTC().Format(time.RFC3339)
			out.ModelsSyncedAt = &str
		}
	}
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
			dp.Entitlement = s.dashEntitlementOf(p.Name())
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

// dashEntitlementOf 把账本现状投影为 providers 页可读视图；Ledger 未装配
// 返回 nil（JSON 省略该字段——不确定不吓人，蓝图纪律）。
func (s *Server) dashEntitlementOf(providerName string) *dashEntitlement {
	if s.router == nil || s.router.Ledger == nil {
		return nil
	}
	e := s.router.EntitlementOf(providerName, "")
	out := &dashEntitlement{
		State:      string(e.State),
		Remaining:  e.Window.Remaining,
		Source:     string(e.Source),
		EvidenceID: e.EvidenceID,
		TermsURL:   e.TermsURL,
	}
	if !e.ObservedAt.IsZero() {
		out.ObservedAt = e.ObservedAt.UTC().Format(time.RFC3339)
	}
	if !e.ValidUntil.IsZero() {
		out.ValidUntil = e.ValidUntil.UTC().Format(time.RFC3339)
	}
	return out
}

// dashKey 是 keys 页的一行；Source 为 stored / env:VAR / none / -。
// SignupURL 是该厂商"申请密钥"官方页（一键抵达；无则空，如 ollama）。
type dashKey struct {
	Provider  string `json:"provider"`
	Source    string `json:"source"`
	Label     string `json:"label,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	SignupURL string `json:"signup_url,omitempty"`
}

// SetSignupURLs 注入 provider → 申请密钥官方页（cmd/ofd 装配期从
// 注册表声明提取；keys 页"获取密钥"列与桌面端「申请密钥」按钮共用）。
func (s *Server) SetSignupURLs(m map[string]string) { s.signupURLs = m }

// SetKeyring 注入密钥环（cmd/ofd 装配期调用）：dashboard 内联添加
// 密钥端点用——与 `ofd key add` 同一加密存储路径。nil = 端点 503。
func (s *Server) SetKeyring(kr *security.Keyring) { s.keyring = kr }

// handleDashboardKeys 合并注入的 key 来源（cmd/ofd 装配期事实）与
// connections 表（stored 记录的 label/updated_at；密文永不离开 store）。
func (s *Server) handleDashboardKeys(w http.ResponseWriter, _ *http.Request) {
	keys := map[string]dashKey{}
	for p, src := range s.keySources {
		keys[p] = dashKey{Provider: p, Source: src, SignupURL: s.signupURLs[p]}
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

// handleDashboardKeysSet 是密钥页/设置面的内联添加密钥端点（用户
// 2026-09-30 需求：行内直接录入，免进设置逐个选）。**仅限 master
// token**（写厂商密钥是高权限操作，scoped token 一律 403）；密钥
// AES-256-GCM 加密入 connections 表，与 `ofd key add` 同一存储路径。
// 密钥明文不落日志/不回显。provider 在路由面生效需重启网关
// （buildRouter 启动期实例化），响应带 restart_required 提示。
func (s *Server) handleDashboardKeysSet(w http.ResponseWriter, r *http.Request) {
	// 双形态取 token（Bearer 头 / ?key=）后精确比对 master——dashboard
	// 页面只能带 ?key=，Bearer-only 会把合法 master 拒成 403。
	tok := tokenFromRequest(r)
	if tok == "" || !tokenEqual(tok, s.gatewayToken) {
		writeAPIError(w, http.StatusForbidden,
			"storing provider keys requires the master gateway key", "permission_error", "insufficient_scope")
		return
	}
	if s.st == nil || s.keyring == nil {
		writeAPIError(w, http.StatusServiceUnavailable,
			"key store not assembled in this gateway", "server_error", "")
		return
	}
	var in struct {
		Provider string `json:"provider"`
		Key      string `json:"key"`
		Label    string `json:"label"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body", "invalid_request_error", "")
		return
	}
	if _, known := s.keySources[in.Provider]; !known {
		writeAPIError(w, http.StatusBadRequest,
			"unknown provider "+in.Provider, "invalid_request_error", "")
		return
	}
	if strings.TrimSpace(in.Key) == "" {
		writeAPIError(w, http.StatusBadRequest, "empty key; nothing stored", "invalid_request_error", "")
		return
	}
	ct, err := s.keyring.Encrypt([]byte(in.Key))
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "encrypt key failed", "server_error", "")
		return
	}
	if err := s.st.SetConnection(in.Provider, ct, in.Label); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "store key failed", "server_error", "")
		return
	}
	if s.log != nil { // 只记 provider 与动作，永不记密钥材料
		s.log.Info("provider key stored via dashboard", "provider", in.Provider)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "provider": in.Provider, "source": "stored", "restart_required": true,
	})
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
