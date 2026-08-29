// ofd — OmniFusion Daemon：BYOK 优先的高性能 AI 网关。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/swsgbl/omnifusion/internal/a2a"
	"github.com/swsgbl/omnifusion/internal/agent"
	"github.com/swsgbl/omnifusion/internal/catalogfeed"
	"github.com/swsgbl/omnifusion/internal/compression"
	"github.com/swsgbl/omnifusion/internal/config"
	"github.com/swsgbl/omnifusion/internal/intelligence"
	"github.com/swsgbl/omnifusion/internal/obs"
	"github.com/swsgbl/omnifusion/internal/security"
	"github.com/swsgbl/omnifusion/internal/server"
	"github.com/swsgbl/omnifusion/internal/store"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "ofd:", err)
		os.Exit(1)
	}
}

// dispatchSubcommand 分发 CLI 子命令：ofd key add|list|remove（M1.7）/
// ofd gateway-key（M1.8）/ ofd status（M2.6）/ ofd mcp（M5.1，stdio MCP
// server）/ ofd mcp-token（M5.2，scoped token 派生）/ ofd run（M5.3，CLI
// 包装）/ ofd catalog（M6.5，签名 feed keygen/sign/verify/report）。
// args 为空返回 (false, nil)：无子命令即进入 serve；未知子命令返回错误
// 而不是静默启动服务（误敲 ofd help 之类不再意外挂起前台进程）。
func dispatchSubcommand(cfg *config.Config, cfgPath string, args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "key":
		return true, runKeyCommand(cfg, args[1:])
	case "gateway-key":
		return true, runGatewayKeyCommand()
	case "status":
		return true, runStatusCommand(cfg)
	case "mcp":
		return true, runMCPCommand(cfg, args[1:])
	case "mcp-token":
		return true, runMCPTokenCommand(args[1:])
	case "run":
		return true, runRunCommand(cfg, cfgPath, args[1:])
	case "catalog":
		return true, runCatalogCommand(cfg, args[1:])
	default:
		return true, fmt.Errorf(
			"unknown subcommand %q; valid: key, gateway-key, status, mcp, mcp-token, run, catalog; omit to serve",
			args[0])
	}
}

func run() error {
	cfgPath := flag.String("config", "", "配置文件路径（默认使用内置默认值）")
	showVersion := flag.Bool("version", false, "打印版本号")
	flag.Parse()
	if *showVersion {
		fmt.Println("ofd", version)
		return nil
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	// 构建链只注入 main.version（goreleaser -X main.version）；server 包
	// 的版本（启动横幅/dashboard/MCP serverInfo）由此转发，单一事实源。
	server.SetVersion(version)

	// CLI 子命令见 dispatchSubcommand；未知子命令报错而非静默进入 serve。
	handled, err := dispatchSubcommand(cfg, *cfgPath, flag.Args())
	if handled {
		return err
	}

	logger, err := obs.New(cfg.Log.Level, cfg.Log.Format)
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}

	if dir := filepath.Dir(cfg.Store.Path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create data dir: %w", err)
		}
	}
	st, err := store.Open(cfg.Store.Path)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	kr, err := security.Open("")
	if err != nil {
		return fmt.Errorf("open keyring: %w", err)
	}

	srv := server.New(cfg, logger, st)
	router, keySources := buildRouter(logger, st, kr)
	srv.SetRouter(router)
	srv.SetKeySources(keySources) // M4.8：Dashboard keys 页的来源描述
	printFirstRunGuide(cfg, keySources) // 小白友好（P0）：零密钥时给三步上手指引

	// M3.5：模型目录——启动立即同步一轮，随后 1h 定时刷新（校验和
	// 判变更才落库）；/v1/models 服务于目录快照。
	catalog := buildCatalog(logger, st, router)
	srv.SetCatalog(catalog)
	go catalog.Run(ctx)

	// M4.5 跨层：路由侧经目录查询 (provider, model) 上下文窗口，
	// 压缩后 token 超窗的候选被排除（压缩把请求缩小后小窗口
	// 免费模型进入候选）。
	router.Windows = catalog

	// 模型成员过滤（docs/00 §4.5 遗留项落地）：裸模型请求只尝试
	// 目录声明可服务该模型的 provider——候选序中被墙/不可达家不再
	// 吃满上游超时才回退（bench 实证每新会话首请求 ~25s）。
	router.Models = catalog

	// quality 策略能力分查询（签名 feed 的 capability 字段是数据源；
	// 无 feed 时 @quality 退化为注册序，不阻断）。
	router.Capability = catalog

	// cheap 策略真成本排序（注册表/签名 feed 登记定价；无数据时
	// cheap 保持 v1 配额余量语义，不阻断）。
	router.Price = catalog

	// M4.6：语义缓存精确层——非流式请求命中直接返回（TTFT<10ms），
	// 未命中上游成功后异步回写；TTL 24h、容量 4096 条（每 64 次回写
	// 触发一次淘汰）。
	srv.SetCache(intelligence.NewSemCache(st, 24*time.Hour, 4096))

	// M4.7：组合层——路由组合（命名模型组，"@combo:NAME" 选择）+
	// 绑定的压缩组合（per-path 压缩策略）。配置语义错误（未知名阶段
	// 等）在此暴露，fail-fast。
	combos, comboPipes, err := buildCombos(cfg)
	if err != nil {
		return fmt.Errorf("build combos: %w", err)
	}
	router.Combos = combos
	srv.SetComboPipelines(comboPipes)
	if len(combos) > 0 {
		logger.Info("combos loaded", "count", len(combos))
	}

	// M6.2：语义压缩参数注入（句保留率钳 [0.1,0.9] + 可选 sidecar
	// URL）。规则档 semantic 阶段零模型依赖恒可用；sidecar 档仅在
	// URL 配置时被 ShouldRun 放行（阶段实例在 buildCombos 构造、
	// 运行时读包级配置）。
	compression.ConfigureSemantic(cfg.Semantic.Rate, cfg.Semantic.SidecarURL)
	if cfg.Semantic.SidecarURL != "" {
		logger.Info("semantic sidecar enabled",
			"url", cfg.Semantic.SidecarURL, "rate", cfg.Semantic.Rate)
	}

	// M6.1：Fusion 扇出合成（"@fusion" 指令；空 members = 未启用，
	// 请求在边界 400）。成员字段/门控区间已由 config.Validate 校验。
	if fr := buildFusion(cfg.Fusion, logger); fr != nil {
		srv.SetFusion(fr)
		logger.Info("fusion enabled",
			"members", len(fr.Members), "quorum", cfg.Fusion.Quorum)
	}

	// M6.3：ML 路由（"@smart" 指令；未配置 mlrouter 段时请求边界
	// 400）。默认纯 Go 启发式难度分档（RouteLLM 思想弱/强二分），
	// ONNX 对比实现走可选构建（ADR-009）。
	if ml := buildMLRouter(cfg.MLRouter); ml != nil {
		attachSmartRouter(router, ml, logger)
		logger.Info("mlrouter enabled",
			"weak", cfg.MLRouter.Weak.Provider+"/"+cfg.MLRouter.Weak.Model,
			"strong", cfg.MLRouter.Strong.Provider+"/"+cfg.MLRouter.Strong.Model,
			"threshold", ml.EffectiveThreshold(), "classifier", "heuristic")
	}

	// M6.4：会话记忆（FTS5 会话记忆）——恒装配、逐请求 opt-in：请求头
	// X-OmniFusion-Memory: on 开启记录与召回，默认关闭 = 零行为变更、
	// 零落盘（隐私红线）。v1 仅非流式路径记录；召回对流式/非流式均生效。
	srv.SetMemory(intelligence.NewMemory(st, logger))
	logger.Info("session memory ready", "opt_in_header", "X-OmniFusion-Memory: on")

	// M6.5：签名 catalog feed——pinned 公钥对原始字节 Ed25519 验签，
	// 失败即丢弃保留旧目录；版本基线持久化（meta 表），等值=重放、
	// 更小=回滚均拒绝。启动立即拉一轮，随后 1h 刷新。
	if cfg.Catalog.FeedURL != "" {
		pub, err := catalogfeed.ParsePublicKey(cfg.Catalog.FeedPubkey)
		if err != nil {
			return fmt.Errorf("parse catalog.feed_pubkey: %w", err)
		}
		ing := catalogfeed.NewIngestor(st, pub, logger)
		applyFeed := func() {
			f, err := ing.Refresh(ctx, cfg.Catalog.FeedURL)
			if err != nil {
				var rb *catalogfeed.RollbackError
				if errors.As(err, &rb) && rb.FeedVersion == rb.Baseline {
					logger.Info("catalog feed unchanged (same version replayed)", "version", rb.FeedVersion)
					return
				}
				logger.Warn("catalog feed rejected; keeping last accepted", "error", err)
				return
			}
			logger.Info("catalog feed applied",
				"version", f.Version, "entries", catalog.ApplyFeed(f))
		}
		applyFeed()
		go func() {
			t := time.NewTicker(catalogfeed.FeedInterval)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					applyFeed()
				case <-ctx.Done():
					return
				}
			}
		}()
		logger.Info("catalog feed enabled",
			"url", cfg.Catalog.FeedURL, "interval", catalogfeed.FeedInterval.String())
	}

	// M5.4：规则型护栏（默认关闭；启用时装配期校验处置/规则名，语义
	// 错误 fail-fast 不静默吞）。PII 默认拦截、注入默认告警放行。
	if cfg.Guardrails.Enabled {
		g, err := security.NewGuardrails(security.GuardrailsOptions{
			PIIAction:       security.Action(cfg.Guardrails.PII.Action),
			InjectionAction: security.Action(cfg.Guardrails.Injection.Action),
			PIITypes:        cfg.Guardrails.PII.Types,
		})
		if err != nil {
			return fmt.Errorf("build guardrails: %w", err)
		}
		srv.SetGuardrails(g)
		logger.Info("guardrails enabled",
			"pii_action", g.PIIAction(), "injection_action", g.InjectionAction())
	}

	// M5.5：Prometheus 指标（默认开启；/metrics 挂网关 key 鉴权）。
	// 审计日志无需装配——server 侧按 cfg.Audit 直写 store。
	if cfg.Metrics.Enabled {
		srv.SetMetrics(obs.NewMetrics())
		logger.Info("metrics endpoint enabled", "path", "/metrics", "auth", "gateway key")
	}

	// M1.8（docs/06 R5 对策 2）：数据面强制网关 key，派生自主密钥，不落盘。
	token, err := kr.GatewayToken()
	if err != nil {
		return fmt.Errorf("derive gateway token: %w", err)
	}
	srv.SetGatewayToken(token)
	logger.Info("gateway API key enforced; clients must send Authorization: Bearer <key> (print with: ofd gateway-key)")

	// M5.1：MCP Streamable HTTP 挂 /mcp（Claude Code 等 MCP 客户端）。
	// 工具数据经 GatewayView 环回访问自身 dashboard API——与 stdio 模式
	// （ofd mcp）共用同一实现与数据口径。
	mcpView := agent.NewGatewayView(
		fmt.Sprintf("http://%s:%d", cfg.Server.Host, cfg.Server.Port), token, nil)
	srv.SetMCPHandler(agent.ScopedHTTPHandler(mcpView, version, srv.ResolveRequestScopes))
	logger.Info("MCP endpoint enabled", "path", "/mcp", "transport", "streamable-http")

	// M7.2：A2A v1.0 协议面——AgentCard 发现 + JSON-RPC /rpc。网关以
	// 无状态代理 agent 形态接入（SendMessage 走 Message-only，流式走
	// 任务生命周期流）；缺省模型可含 @smart/@fusion/@combo 指令。
	if cfg.A2A.Enabled {
		base := fmt.Sprintf("http://%s:%d", cfg.Server.Host, cfg.Server.Port)
		if host := cfg.Server.Host; host == "" || host == "0.0.0.0" || host == "::" {
			base = fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port)
		}
		srv.SetA2A(a2a.BuildCard(a2a.CardOptions{
			BaseURL:      base,
			Name:         cfg.A2A.Name,
			Description:  cfg.A2A.Description,
			Version:      version,
			DefaultModel: cfg.A2A.DefaultModel,
			Streaming:    true,
		}), cfg.A2A.DefaultModel)
		logger.Info("A2A agent endpoints enabled",
			"card", "/.well-known/agent-card.json", "rpc", "/rpc",
			"default_model", cfg.A2A.DefaultModel)
	}

	if ip := net.ParseIP(cfg.Server.Host); ip == nil || !ip.IsLoopback() {
		logger.Warn("⚠ listening on a NON-loopback address: the gateway is reachable from the network; " +
			"keep the gateway key secret and prefer a reverse proxy with TLS in front of it")
	}

	return srv.ListenAndServe(ctx)
}
