// scope.go 是作用域权限核心（M5.2 四个 + M5.5 audit）：五个 scope（对齐
// 工具——健康查询/用量统计/路由切换/压缩配置）+ scoped token 的
// 无存储派生（HMAC）与解析 + scope 化鉴权中间件。
//
// Token 两级：master gateway key（ofg-…，keyring 派生）拥有全部
// scope（向后兼容 M4.8 Dashboard 与 M5.1）；scoped token（ofm-…，
// `ofd mcp-token --scopes …` 生成）只拥有声明子集。验证无需存储：
// 枚举 scope 非空子集重算 HMAC 比对（4 scope → 15 次，常数时间）。
package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
)

// Scope 常量（M5.2）：dashboard API 端点与 MCP 工具共用的权限维度。
const (
	ScopeHealth      = "health"      // 健康查询：providers/keys/models/health
	ScopeUsage       = "usage"       // 用量统计：usage
	ScopeRoute       = "route"       // 路由切换：pin/unpin/隔离清除
	ScopeCompression = "compression" // 压缩配置：combos/默认组合
	ScopeAudit       = "audit"       // 请求审计：audit 查询（M5.5）
)

// AllScopes 是 scope 全集（master token 的权限面）。
var AllScopes = []string{ScopeHealth, ScopeUsage, ScopeRoute, ScopeCompression, ScopeAudit}

// scopedTokenPrefix 区分派生 token（master 网关 key 是 ofg- 前缀）。
const scopedTokenPrefix = "ofm-"

// DeriveMCPToken 从 master gateway key 派生 scoped token：
// ofm- + hex(HMAC-SHA256(master, "mcp-token:"+join(sorted(scopes))))[:20]。
// 确定性派生：同 master + 同 scope 集合恒得同 token，无存储、随时
// 可重新生成（作废只能换 master key）。scopes 会归一（去重/排序/
// 剔除未知）；空集返回空串。
func DeriveMCPToken(master string, scopes []string) string {
	sorted := normalizeScopes(scopes)
	if len(sorted) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(master))
	mac.Write([]byte("mcp-token:" + strings.Join(sorted, ",")))
	return scopedTokenPrefix + hex.EncodeToString(mac.Sum(nil))[:20]
}

// normalizeScopes 剔除未知 scope、去重并排序（HMAC 输入归一）。
// 导出形态 NormalizeScopes 供 CLI 展示归一结果（cmd/ofd mcp-token）。
func NormalizeScopes(scopes []string) []string { return normalizeScopes(scopes) }

func normalizeScopes(scopes []string) []string {
	seen := make(map[string]bool, len(scopes))
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		if !knownScope(s) || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func knownScope(s string) bool {
	for _, k := range AllScopes {
		if k == s {
			return true
		}
	}
	return false
}

// ResolveScopes 解析 token 的 scope 集：master → 全集；合法 scoped
// token → 派生子集；其余（未知/伪造）第二返回值 false。
func ResolveScopes(master, token string) ([]string, bool) {
	if master == "" || token == "" {
		return nil, false
	}
	if tokenEqual(token, master) {
		return append([]string(nil), AllScopes...), true
	}
	if !strings.HasPrefix(token, scopedTokenPrefix) {
		return nil, false
	}
	n := len(AllScopes)
	for mask := 1; mask < 1<<n; mask++ {
		sub := make([]string, 0, n)
		for i := 0; i < n; i++ {
			if mask&(1<<i) != 0 {
				sub = append(sub, AllScopes[i])
			}
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(DeriveMCPToken(master, sub))) == 1 {
			return sub, true
		}
	}
	return nil, false
}

// tokenFromRequest 提取 dashboard 家族端点的令牌（Bearer 头与 ?key=
// 双形态——浏览器页面无法带自定义头）。
func tokenFromRequest(r *http.Request) string {
	if tok := r.URL.Query().Get("key"); tok != "" {
		return tok
	}
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, bearerPrefix) {
		return strings.TrimPrefix(h, bearerPrefix)
	}
	return ""
}

// ResolveRequestScopes 是 scope 解析的请求形态（Streamable HTTP MCP
// 按 token 构造 per-scope server 时由 main 注入 agent 包）。
func (s *Server) ResolveRequestScopes(r *http.Request) ([]string, bool) {
	if s.gatewayToken == "" {
		return nil, false
	}
	return ResolveScopes(s.gatewayToken, tokenFromRequest(r))
}

// requireScope 是 scope 化鉴权中间件（M5.2 dashboard API 逐端点）：
// master 全通过；scoped token 须含该 scope——否则 403（越权明确
// 拒绝）；未知 token 401；未装配 master fail-closed。
func (s *Server) requireScope(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scopes, ok := s.ResolveRequestScopes(r)
		if !ok {
			writeAPIError(w, http.StatusUnauthorized,
				"missing or invalid gateway API key; send 'Authorization: Bearer <key>' or '?key=<key>' (see: ofd gateway-key)",
				"authentication_error", "")
			return
		}
		for _, sc := range scopes {
			if sc == scope {
				next.ServeHTTP(w, r)
				return
			}
		}
		writeAPIError(w, http.StatusForbidden,
			"token scope insufficient for this endpoint (requires "+scope+")",
			"permission_error", "insufficient_scope")
	})
}

// requireAnyScope 放行任意合法 token（master 或 scoped）：/mcp 与
// whoami 端点用——工具/数据可见性由 MCP server 构造层与各端点的
// requireScope 分别收敛，这里只挡伪造 token。
func (s *Server) requireAnyScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.ResolveRequestScopes(r); !ok {
			writeAPIError(w, http.StatusUnauthorized,
				"missing or invalid gateway API key; send 'Authorization: Bearer <key>' (see: ofd gateway-key / ofd mcp-token)",
				"authentication_error", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}
