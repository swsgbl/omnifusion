// Package server 装配 L1 Core 的 HTTP 服务。
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/swsgbl/omnifusion/internal/a2a"
	"github.com/swsgbl/omnifusion/internal/compression"
	"github.com/swsgbl/omnifusion/internal/config"
	"github.com/swsgbl/omnifusion/internal/intelligence"
	"github.com/swsgbl/omnifusion/internal/obs"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/security"
	"github.com/swsgbl/omnifusion/internal/store"
)

// Server 持有全部层依赖；随里程碑扩展字段（路由、压缩、缓存…）。
type Server struct {
	cfg          *config.Config
	log          *slog.Logger
	st           *store.Store
	router       *routing.Router
	catalog      *routing.Catalog                 // 模型目录（；nil = /v1/models 给空表）
	cache        *intelligence.SemCache           // 语义缓存（；nil = 不查不写）
	fusion       *intelligence.FusionRunner       // Fusion 扇出合成（；nil = @fusion 边界 400）
	memory       *intelligence.Memory             // FTS5 会话记忆（；nil = opt-in 头无效果）
	comboPipes   map[string]*compression.Pipeline // 组合压缩管线（；键存在值可 nil=纯路由组合）
	keySources   map[string]string                // key 来源描述（Dashboard；provider → stored/env:VAR/none/-）
	mcpHandler   http.Handler                     // MCP Streamable HTTP（；nil = 不挂 /mcp）
	guard        *security.Guardrails             // 规则型护栏（；nil = 未启用）
	metrics      *obs.Metrics                     // Prometheus 指标（；nil = 未启用，全 no-op）
	gatewayToken string

	a2aCard  *a2a.AgentCard // A2A 发现清单（；nil = 不挂 agent-card 与 /rpc）
	a2aModel string         // A2A 缺省目标模型（可含 @指令）

	pinMu    sync.Mutex             // 全局路由钉选
	pinName  string                 // 钉选 provider；空 = 未钉
	pinUntil time.Time              // 钉选到期时刻（TTL，默认 30m）
	defCombo atomic.Pointer[string] // 默认压缩组合（；nil/空 = 未设）
	cstats   comboStats             // 压缩统计聚合（；零值可用，恒装配）
}

// New 装配 Server。
func New(cfg *config.Config, log *slog.Logger, st *store.Store) *Server {
	return &Server{cfg: cfg, log: log, st: st}
}

// SetRouter 注入分发器（provider 装配在 main，见 cmd/ofd）。
func (s *Server) SetRouter(r *routing.Router) { s.router = r }

// SetCatalog 注入模型目录。
func (s *Server) SetCatalog(c *routing.Catalog) { s.catalog = c }

// SetCache 注入语义缓存。未装配时三端点非流式路径直通上游。
func (s *Server) SetCache(c *intelligence.SemCache) { s.cache = c }

// SetFusion 注入 Fusion 扇出合成器。未装配时 "@fusion" 请求
// 在边界 400（不静默降级为普通分发）。
func (s *Server) SetFusion(f *intelligence.FusionRunner) { s.fusion = f }

// SetMemory 注入 FTS5 会话记忆。记忆逐请求 opt-in（头
// X-OmniFusion-Memory: on）；未装配或头缺席均不查不写。
func (s *Server) SetMemory(m *intelligence.Memory) { s.memory = m }

// SetComboPipelines 注入组合压缩管线：键是组合名，值 nil 表示
// 纯路由组合（不压缩）。键集合即「已知组合名」，未知名在边界 400。
func (s *Server) SetComboPipelines(pipes map[string]*compression.Pipeline) { s.comboPipes = pipes }

// SetKeySources 注入 provider → key 来源描述（Dashboard keys 页）：
// "stored" / "env:VAR" / "none" / "-"。凭据存在性在 cmd/ofd 装配期
// 判定（env 读取在那一层），server 只展示。
func (s *Server) SetKeySources(src map[string]string) { s.keySources = src }

// SetMCPHandler 注入 MCP Streamable HTTP 传输（挂 /mcp）；
// 未装配时不注册该路由。鉴权在路由层复用网关 key（Bearer）。
func (s *Server) SetMCPHandler(h http.Handler) { s.mcpHandler = h }

// SetGuardrails 注入规则型护栏：三协议入站在翻译后、分发前
// 扫描正文。未装配（nil）即未启用。
func (s *Server) SetGuardrails(g *security.Guardrails) { s.guard = g }

// SetMetrics 注入 Prometheus 指标：/metrics 端点与请求出账
// （recordRequest）。未装配（nil）时全部指标调用零开销、无 /metrics 路由。
func (s *Server) SetMetrics(m *obs.Metrics) { s.metrics = m }

// SetGatewayToken 装配数据面鉴权令牌（R5 对策 2：强制、非可选）。
// 未装配时数据面 fail-closed：一律 401。
func (s *Server) SetGatewayToken(token string) { s.gatewayToken = token }

// Handler 构建完整 HTTP 路由。
// 数据面（/v1/**）强制网关 key；控制面探针 /healthz 保持开放。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /{$}", s.handleRoot) // 根路径双语落地页（无敏感信息，与 /healthz 同级开放）
	mux.Handle("GET /v1/models", // 模型目录（catalog 快照）
		s.requireGatewayKey(http.HandlerFunc(s.handleModels)))
	mux.Handle("POST /v1/chat/completions",
		s.requireGatewayKey(http.HandlerFunc(s.handleChatCompletions)))
	mux.Handle("POST /v1/responses", // Responses API 入站（Codex CLI 默认 wire 协议）
		s.requireGatewayKey(http.HandlerFunc(s.handleResponses)))
	mux.Handle("POST /v1/messages", // Anthropic 入站（Claude Code 直连）
		s.requireAnthropicKey(http.HandlerFunc(s.handleMessages)))
	mux.Handle("POST /v1beta/models/", // Gemini 入站（通配符后不能跟字面后缀，handler 内拆路径）
		s.requireGeminiKey(http.HandlerFunc(s.handleGeminiGenerateContent)))
	mux.HandleFunc("GET /dashboard", s.handleDashboardRoot) // 落到默认页（透传 ?key=）
	mux.Handle("/dashboard/api/",                           // 控制面 JSON API：外层挡伪造 token，内层逐端点 requireScope（control_api.go）
		s.requireAnyScope(http.HandlerFunc(s.handleDashboardAPI)))
	mux.Handle("/dashboard/", // Dashboard HTML 页面（master key；与 api/ 同为前缀模式，方法在 handler 内收敛）
		s.requireDashboardKey(http.HandlerFunc(s.handleDashboard)))
	if s.mcpHandler != nil { // MCP Streamable HTTP：Claude Code 等 MCP 客户端入口
		mux.Handle("/mcp", s.requireAnyScope(s.mcpHandler)) // scoped token 亦可入（工具可见性按 scope 收敛）
	}
	if s.metrics != nil { // Prometheus 指标：网关 key 鉴权，不新开匿名信息面
		mux.Handle("GET /metrics", s.requireGatewayKey(s.metrics.Handler()))
	}
	if s.a2aCard != nil { // A2A v1.0：发现清单公开；/rpc 强制网关 key
		mux.HandleFunc("GET /.well-known/agent-card.json", s.handleA2ACard)
		mux.Handle("POST /rpc", s.requireGatewayKey(http.HandlerFunc(s.handleA2ARPC)))
	}
	return obs.RequestLogger(s.log)(mux)
}

// ListenAndServe 启动并支持 ctx 取消后的优雅关停。
func (s *Server) ListenAndServe(ctx context.Context) error {
	addr := net.JoinHostPort(s.cfg.Server.Host, strconv.Itoa(s.cfg.Server.Port))
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	s.log.Info("listening", "addr", addr, "version", version)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		s.log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
