// dashboard.go 承载 Dashboard v0：三张自包含静态 HTML（go:embed，
// 无 Vite/React 构建链——个人网关的最小可用观测面）+ JSON 活数据。
// 页面本身用 fetch 相对路径 api/<page>?key=… 取数，浏览器无法带
// Bearer 头，故鉴权同时接受 ?key= 查询参数（默认回环监听可接受）。
package server

import (
	"embed"
	"net/http"
	"strings"
)

// dashboardFS 内嵌三张页面（providers / keys / usage）。
//
//go:embed dashboard/*.html
var dashboardFS embed.FS

// requireDashboardKey 是 Dashboard 控制面鉴权：网关 key 的 Bearer 形态
// （脚本/监控）与 ?key= 形态（浏览器页面）都收；未装配 token 时
// fail-closed，一律 401。浏览器导航（Accept 含 text/html）回双语
// HTML 指引页而非裸 JSON——iframe 直开页面时的小白体验。
func (s *Server) requireDashboardKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.dashboardKeyOK(r) {
			if strings.Contains(r.Header.Get("Accept"), "text/html") {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(dashboardKeyHTML))
				return
			}
			writeAPIError(w, http.StatusUnauthorized,
				"missing or invalid gateway API key; send 'Authorization: Bearer <key>' or '?key=<key>' (see: ofd gateway-key)",
				"authentication_error", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// dashboardKeyHTML 是浏览器形态 401 的双语指引页（不回显任何令牌）。
const dashboardKeyHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>OmniFusion · 需要网关令牌 / Token required</title>
<style>
  :root { color-scheme: dark; }
  body { font: 15px/1.8 system-ui, sans-serif; background: #0d1117; color: #e6edf3;
         margin: 0; min-height: 100vh; display: grid; place-items: center; }
  .card { max-width: 520px; padding: 32px 36px; border: 1px solid #30363d;
          border-radius: 12px; background: #161b22; }
  h1 { font-size: 18px; margin: 0 0 12px; }
  code { background: #0d1117; border: 1px solid #30363d; border-radius: 4px;
         padding: 1px 6px; font-size: 12.5px; }
  .muted { color: #8b949e; font-size: 13px; }
</style>
</head>
<body>
<div class="card">
  <h1>🔑 需要网关令牌 · Gateway token required</h1>
  <p>桌面端：点上方「从 ofd 读取 Key」自动填入，页面会自动重载。</p>
  <p class="muted">Desktop: click "Read Key from ofd" — the page reloads with the key filled in.</p>
  <p>浏览器直开：在 URL 后追加 <code>?key=&lt;令牌&gt;</code>；令牌在终端执行 <code>ofd gateway-key</code> 获取。</p>
  <p class="muted">Direct browser access: append <code>?key=&lt;token&gt;</code> to the URL; get the token with <code>ofd gateway-key</code>.</p>
</div>
</body>
</html>`

// dashboardKeyOK 取 ?key= 或 Bearer 之一做常数时间比较。
func (s *Server) dashboardKeyOK(r *http.Request) bool {
	if s.gatewayToken == "" {
		return false
	}
	if tok := r.URL.Query().Get("key"); tok != "" {
		return tokenEqual(tok, s.gatewayToken)
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, bearerPrefix) {
		return false
	}
	return tokenEqual(strings.TrimPrefix(h, bearerPrefix), s.gatewayToken)
}

// handleDashboard 是 /dashboard/ 前缀的 HTML 页面分发（三页 +
// compression/resilience 两页 + 对话页 chat + 随后与 JSON API
// 分离）：{providers,keys,usage,compression,resilience,chat} 出 HTML，
// 其余 404；同时收 ".html" 后缀形态（页面互链用 providers.html 相对
// 链接——第三轮小白友好审计发现的历史 404）。JSON 控制面在
// /dashboard/api/ 前缀（scope 鉴权，control_api.go）。
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { // 前缀模式与 api/ 共存故不能在路由层限定方法
		methodNotAllowed(w, http.MethodGet)
		return
	}
	rest := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/dashboard/"), ".html")
	switch rest {
	case "providers", "keys", "usage", "compression", "resilience", "chat":
		s.serveDashboardPage(w, rest)
	default:
		http.NotFound(w, r)
	}
}

// serveDashboardPage 输出一张内嵌页面。
func (s *Server) serveDashboardPage(w http.ResponseWriter, page string) {
	data, err := dashboardFS.ReadFile("dashboard/" + page + ".html")
	if err != nil {
		http.Error(w, "dashboard page unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleDashboardRoot 把 /dashboard 落到默认页（透传查询参数——key
// 在 query 里，丢了会直接 401）。
func (s *Server) handleDashboardRoot(w http.ResponseWriter, r *http.Request) {
	target := "/dashboard/providers"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusFound)
}
