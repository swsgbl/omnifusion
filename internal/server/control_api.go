// control_api.go 是 控制面：dashboard API 的 scope 化扩展——
// 读端点（whoami/models/health/combos/route status）与写端点
// （route pin/unpin/隔离清除、默认压缩组合）。外层 requireAnyScope
// 挡伪造 token（401），内层逐端点 requireScope 收敛权限（越权 403）。
package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// handleDashboardAPI 是 /dashboard/api/ 的总分发（方法+端点双维，
// scope 鉴权逐端点应用；HTML 页面不走此路径）。
func (s *Server) handleDashboardAPI(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/dashboard/api/")
	if rest == r.URL.Path { // 非 /dashboard/api/ 前缀（不会到达，防御）
		http.NotFound(w, r)
		return
	}
	switch rest {
	case "whoami":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		s.handleWhoami(w, r)
	case "providers":
		s.scopeGuard(w, r, ScopeHealth, http.MethodGet, s.handleDashboardProviders)
	case "keys":
		s.scopeGuard(w, r, ScopeHealth, http.MethodGet, s.handleDashboardKeys)
	case "models":
		s.scopeGuard(w, r, ScopeHealth, http.MethodGet, s.handleDashboardModels)
	case "health":
		s.scopeGuard(w, r, ScopeHealth, http.MethodGet, s.handleDashboardHealth)
	case "usage":
		s.scopeGuard(w, r, ScopeUsage, http.MethodGet, s.handleDashboardUsage)
	case "audit":
		s.scopeGuard(w, r, ScopeAudit, http.MethodGet, s.handleDashboardAudit)
	case "combos":
		s.scopeGuard(w, r, ScopeCompression, http.MethodGet, s.handleDashboardCombos)
	case "compression/stats":
		s.scopeGuard(w, r, ScopeCompression, http.MethodGet, s.handleCompressionStats)
	case "resilience":
		s.scopeGuard(w, r, ScopeRoute, http.MethodGet, s.handleResilience)
	case "route/status":
		s.scopeGuard(w, r, ScopeRoute, http.MethodGet, s.handleRouteStatus)
	case "route/pin":
		s.scopeGuard(w, r, ScopeRoute, http.MethodPost, s.handleRoutePin)
	case "route/unpin":
		s.scopeGuard(w, r, ScopeRoute, http.MethodPost, s.handleRouteUnpin)
	case "route/cooldowns/clear":
		s.scopeGuard(w, r, ScopeRoute, http.MethodPost, s.handleCooldownsClear)
	case "compression/default":
		s.scopeGuard(w, r, ScopeCompression, http.MethodPost, s.handleCompressionDefault)
	case "butler/ai-tools":
		s.scopeGuard(w, r, ScopeHealth, http.MethodGet, s.handleButlerScan)
	case "butler/wire-ai-tool":
		s.scopeGuard(w, r, ScopeRoute, http.MethodPost, s.handleButlerWire)
	case "butler/unwire-ai-tool":
		s.scopeGuard(w, r, ScopeRoute, http.MethodPost, s.handleButlerUnwire)
	case "butler/find-tool":
		s.scopeGuard(w, r, ScopeHealth, http.MethodPost, s.handleButlerFind)
	case "butler/read-file":
		s.scopeGuard(w, r, ScopeHealth, http.MethodPost, s.handleButlerRead)
	case "butler/edit-file":
		s.scopeGuard(w, r, ScopeRoute, http.MethodPost, s.handleButlerEdit)
	case "butler/write-file":
		s.scopeGuard(w, r, ScopeRoute, http.MethodPost, s.handleButlerWrite)
	case "butler/patch-config":
		s.scopeGuard(w, r, ScopeRoute, http.MethodPost, s.handleButlerPatch)
	case "butler/run-command":
		s.scopeGuard(w, r, ScopeRoute, http.MethodPost, s.handleButlerRunCommand)
	case "butler/web-fetch":
		s.scopeGuard(w, r, ScopeHealth, http.MethodPost, s.handleButlerWebFetch)
	default:
		http.NotFound(w, r)
	}
}

// scopeGuard 组合 method 检查与 scope 鉴权（总分发的逐端点形态）。
func (s *Server) scopeGuard(w http.ResponseWriter, r *http.Request, scope, method string, h http.HandlerFunc) {
	if r.Method != method {
		methodNotAllowed(w, method)
		return
	}
	s.requireScope(scope, h).ServeHTTP(w, r)
}

// methodNotAllowed 输出 405。
func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error", "")
}

// handleWhoami 返回当前 token 的身份与 scope 集（stdio MCP 启动时
// 以此确定工具集；外层 requireAnyScope 已保证合法）。
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	scopes, _ := s.ResolveRequestScopes(r)
	kind := "scoped"
	if len(scopes) == len(AllScopes) {
		kind = "master"
	}
	writeJSON(w, http.StatusOK, map[string]any{"kind": kind, "scopes": scopes})
}

// handleDashboardModels 返回模型目录快照（health scope）。
func (s *Server) handleDashboardModels(w http.ResponseWriter, _ *http.Request) {
	type modelRow struct {
		Provider      string `json:"provider"`
		ID            string `json:"id"`
		ContextWindow int64  `json:"context_window"`
	}
	models := []modelRow{}
	if s.catalog != nil {
		for _, e := range s.catalog.Snapshot() {
			models = append(models, modelRow{Provider: e.Provider, ID: e.ID, ContextWindow: e.CtxLen})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// handleDashboardHealth 返回聚合健康快照（health scope；钉选属 route
// scope，不向 health 泄露——scope 间信息隔离）。
func (s *Server) handleDashboardHealth(w http.ResponseWriter, _ *http.Request) {
	providers := 0
	if s.router != nil {
		providers = len(s.router.Providers)
	}
	cooldowns := 0
	for _, list := range s.activeCooldowns() {
		cooldowns += len(list)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":           "ok",
		"version":          version,
		"providers":        providers,
		"active_cooldowns": cooldowns,
	})
}

// handleDashboardCombos 返回组合清单与压缩绑定（compression scope）。
func (s *Server) handleDashboardCombos(w http.ResponseWriter, _ *http.Request) {
	type comboRow struct {
		Name     string   `json:"name"`
		Stages   []string `json:"stages,omitempty"` // 绑定的压缩阶段；空 = 纯路由组合
		Compress bool     `json:"compress"`
	}
	combos := []comboRow{}
	for name, pipe := range s.comboPipes {
		row := comboRow{Name: name, Compress: pipe != nil}
		if pipe != nil {
			row.Stages = pipe.StageNames()
		}
		combos = append(combos, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"combos": combos, "default_combo": s.defaultCombo()})
}

// pinStatusJSON 是钉选状态的 JSON 形态（route status 与 pin 写端点共用）。
func (s *Server) pinStatusJSON() map[string]any {
	name, until := s.pinSnapshot()
	if name == "" {
		return map[string]any{"pinned": "", "until": nil}
	}
	return map[string]any{"pinned": name, "until": until.UTC().Format(time.RFC3339)}
}

// handleRouteStatus 返回钉选与活跃隔离（route scope）。
func (s *Server) handleRouteStatus(w http.ResponseWriter, _ *http.Request) {
	out := s.pinStatusJSON()
	active := map[string]int{}
	for p, list := range s.activeCooldowns() {
		active[p] = len(list)
	}
	out["active_cooldowns"] = active
	writeJSON(w, http.StatusOK, out)
}

// pinRequest 是 route/pin 与 route/unpin 的请求体。
type pinRequest struct {
	Provider   string `json:"provider"`
	TTLSeconds int    `json:"ttl_seconds"`
}

// handleRoutePin 设置全局钉选（provider 空 = 清除；ttl<=0 用默认 30m）。
func (s *Server) handleRoutePin(w http.ResponseWriter, r *http.Request) {
	var req pinRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error", "")
		return
	}
	if req.Provider != "" && !s.providerKnown(req.Provider) {
		writeAPIError(w, http.StatusBadRequest, "unknown provider "+req.Provider, "invalid_request_error", "")
		return
	}
	s.setPin(req.Provider, time.Duration(req.TTLSeconds)*time.Second)
	writeJSON(w, http.StatusOK, s.pinStatusJSON())
}

// handleRouteUnpin 清除全局钉选。
func (s *Server) handleRouteUnpin(w http.ResponseWriter, _ *http.Request) {
	s.setPin("", 0)
	writeJSON(w, http.StatusOK, s.pinStatusJSON())
}

// providerKnown 判定 provider 是否已装配。
func (s *Server) providerKnown(name string) bool {
	if s.router == nil {
		return false
	}
	for _, p := range s.router.Providers {
		if p.Name() == name {
			return true
		}
	}
	return false
}

// clearRequest 是 route/cooldowns/clear 的请求体。
type clearRequest struct {
	Provider string `json:"provider"`
}

// handleCooldownsClear 人工清除一个 provider 的全部隔离（内存+持久）。
func (s *Server) handleCooldownsClear(w http.ResponseWriter, r *http.Request) {
	var req clearRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error", "")
		return
	}
	if req.Provider == "" {
		writeAPIError(w, http.StatusBadRequest, "provider is required", "invalid_request_error", "")
		return
	}
	cleared := 0
	if s.router != nil && s.router.Isolation != nil {
		cleared = s.router.Isolation.Clear(req.Provider)
	}
	writeJSON(w, http.StatusOK, map[string]any{"provider": req.Provider, "cleared": cleared})
}

// comboDefaultRequest 是 compression/default 的请求体。
type comboDefaultRequest struct {
	Combo string `json:"combo"`
}

// handleCompressionDefault 设置/清除默认压缩组合（未知组合拒绝——
// 否则所有无指令请求都会 400）。
func (s *Server) handleCompressionDefault(w http.ResponseWriter, r *http.Request) {
	var req comboDefaultRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error", "")
		return
	}
	if req.Combo != "" && !s.comboKnown(req.Combo) {
		writeAPIError(w, http.StatusBadRequest, "unknown combo "+req.Combo, "invalid_request_error", "")
		return
	}
	s.setDefaultCombo(req.Combo)
	writeJSON(w, http.StatusOK, map[string]any{"default_combo": s.defaultCombo()})
}
