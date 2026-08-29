// Package routing is L3：把归一化请求分发到有序候选 provider 列表。
// M1 用固定顺序（注册序）；M2 起接入 Strategy 排序、隔离状态机与配额。
package routing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
)

// Attempt 记录一次 provider 尝试的结果，供观测与错误分类（M1.6+）。
type Attempt struct {
	Provider string
	// Model 是实际发往该上游的模型 id（别名重写后）。
	Model string
	// Err 非 nil 表示该次尝试失败。
	Err error
	// Kind 是 Err 的归一化类别（M2.1 Classify 结果；成功为空）。
	Kind ErrorKind
	// Degraded 是该次出站翻译丢弃的请求字段（M3.6）；server 并入
	// X-OmniFusion-Degraded 头。仅成功尝试的清单有效。
	Degraded []string
}

// DispatchError 表示全部候选耗尽后的聚合失败。
type DispatchError struct {
	Attempts []Attempt
}

// Error implements error.
func (e *DispatchError) Error() string {
	if len(e.Attempts) == 0 {
		return "routing: no attempts recorded"
	}
	last := e.Attempts[len(e.Attempts)-1]
	return fmt.Sprintf("routing: all %d provider attempt(s) failed; last: %s: %v",
		len(e.Attempts), last.Provider, last.Err)
}

// Router 按固定顺序尝试候选 provider：Translate → HTTP → Parse；
// 任一环节失败即切换下一家（buffer-first-chunk 之前的阶段都允许切换，
// 见 docs/01 item12 / docs/04 §4.2）。
type Router struct {
	Providers []provider.Provider
	Log       *slog.Logger
	// Isolation 是三层隔离状态机（M2.2）；nil 表示未启用（M1 行为）。
	Isolation *Isolation
	// Quota 是 per-key 配额滑动窗口（M2.3）；nil 表示不预防性限流。
	Quota *QuotaTracker
	// Scoring 是打分排序器（M2.4）；nil 表示固定注册序（M1 行为）。
	Scoring *Scorer
	// Sessions 是 sticky session 绑定表（M2.7）；nil 表示不启用。
	Sessions *SessionTracker
	// Windows 是 (provider, model) 上下文窗口查询（M4.5 跨层）；
	// nil 表示不过滤（无目录时的降级行为）。
	Windows WindowResolver
	// Models 是 (provider, model) 可服务性判定（模型成员过滤，
	// docs/00 §4.5 遗留项落地）：裸模型请求排除目录明确不服务的
	// 候选；nil 表示不过滤。生产装配同 Windows（Catalog 双实现）。
	Models ModelMembership
	// Combos 是命名模型组（M4.7）：model 内嵌 "@combo:NAME" 选择；
	// nil/未收录名 = 普通分发。装配自 YAML（cmd/ofd）。
	Combos map[string]Combo
	// Smart 是 ML 路由计划函数（M6.3 "@smart" 指令）：输入归一化请求，
	// 输出弱/强分档的尝试计划（主档在前）。nil = 未装配（请求边界 400）。
	// 装配自 cmd/ofd（intelligence.MLRouter 适配；L5/L3 互不 import）。
	Smart func(req *schema.UnifiedRequest) SmartPlan
	// Capability 是 (provider, model) → 社区能力分查询（quality 策略，
	// 2026-08-29 补齐）；nil/无数据 = quality 退化为注册序。生产装配
	// 同 Windows/Models（Catalog 三实现，数据源是签名 feed）。
	Capability CapabilityResolver
	// Price 是 (provider, model) → 登记定价查询（cheap 策略真成本
	// 排序，2026-08-30 升级；定价登记于注册表 YAML / 签名 feed）；
	// nil/无数据 = cheap 退化为 v1 配额余量语义。生产装配同
	// Capability（Catalog 实现：feed 优先、注册表静态兜底）。
	Price PriceResolver
}

// Dispatch 执行分发，成功时返回聚合响应与全部尝试记录；
// opts 可逐请求覆盖策略（WithStrategyName，M2.5）。
func (r *Router) Dispatch(ctx context.Context, req *schema.UnifiedRequest, opts ...DispatchOption) (*schema.Response, []Attempt, error) {
	if len(r.Providers) == 0 {
		return nil, nil, errors.New("routing: no providers configured")
	}
	cfg := resolveOptions(opts)
	if cfg.targetProvider != "" { // M6.1 定向分发（Fusion 原语）
		return r.dispatchTarget(ctx, cfg, req)
	}
	cands := r.candidatesFor(cfg, req) // M6.3：smart 指令在此分流到 ML 计划
	attempts := make([]Attempt, 0, len(r.Providers))
	for _, c := range cands {
		if ctx.Err() != nil {
			break
		}
		if att, skipped := r.skipIfBlocked(c.p); skipped {
			attempts = append(attempts, att)
			continue
		}
		start := time.Now()
		resp, att := r.tryOne(ctx, c.p, c.model, req)
		r.Scoring.Observe(c.p.Name(), time.Since(start), att.Err == nil)
		att.Kind = Classify(att.Err)
		attempts = append(attempts, att)
		if att.Err == nil {
			r.applyIsolation(c.p.Name(), att)
			r.recordQuota(c.p.Name(), resp)
			r.bindSession(cfg.sessionID, c.p.Name())
			return resp, attempts, nil
		}
		r.applyIsolation(c.p.Name(), att)
		if r.Log != nil {
			r.Log.Warn("provider attempt failed",
				"provider", att.Provider, "model", att.Model, "kind", att.Kind, "err", att.Err)
		}
	}
	return nil, attempts, &DispatchError{Attempts: attempts}
}

// tryOne 对单个候选执行 Translate → HTTP → Parse；model 是实际发往
// 该上游的模型名（组合成员模型，M4.7）——浅拷贝请求改写，不污染
// 调用方的 req（缓存键/日志面仍取原始 Model）。
func (r *Router) tryOne(ctx context.Context, p provider.Provider, model string, req *schema.UnifiedRequest) (*schema.Response, Attempt) {
	att := Attempt{Provider: p.Name()}

	perAttempt := *req
	perAttempt.Model = model
	call, err := p.Translate(ctx, &perAttempt)
	if err != nil {
		att.Err = fmt.Errorf("translate: %w", err)
		return nil, att
	}
	att.Model = call.Model
	att.Degraded = call.Degraded

	// Model 层隔离在别名重写后才能判定（锁定键是 provider 侧模型名）。
	if r.Isolation != nil {
		if locked, reason := r.Isolation.LockedModel(p.Name(), call.Model); locked {
			att.Err = fmt.Errorf("routing: model isolated (%s)", reason)
			return nil, att
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, call.Method, call.URL, bytes.NewReader(call.Body))
	if err != nil {
		att.Err = fmt.Errorf("build request: %w", err)
		return nil, att
	}
	httpReq.Header = call.Header

	upstreamResp, err := p.HTTPClient().Do(httpReq)
	if err != nil {
		att.Err = fmt.Errorf("upstream transport: %w", err)
		return nil, att
	}

	parsed, err := p.Parse(ctx, call, upstreamResp)
	if err != nil {
		att.Err = err
		return nil, att
	}
	return parsed, att
}

// dispatchTarget 定向分发（M6.1 Fusion 扇出/合成原语）：跳过候选选择
// 与策略排序，直接尝试 (targetProvider, targetModel)。隔离/配额/打分
// 观测与普通路径完全一致；无 failover（定向语义：成员失败由调用方
// Fusion 层的门控/降级接管）。
func (r *Router) dispatchTarget(ctx context.Context, cfg dispatchConfig, req *schema.UnifiedRequest) (*schema.Response, []Attempt, error) {
	attempts := make([]Attempt, 0, 1)
	p := r.providerByName(cfg.targetProvider)
	if p == nil {
		return nil, append(attempts, Attempt{
			Provider: cfg.targetProvider,
			Model:    cfg.targetModel,
			Err:      fmt.Errorf("routing: target provider %q not configured", cfg.targetProvider),
		}), &DispatchError{Attempts: attempts}
	}
	if att, skipped := r.skipIfBlocked(p); skipped {
		attempts = append(attempts, att)
		return nil, attempts, &DispatchError{Attempts: attempts}
	}
	start := time.Now()
	resp, att := r.tryOne(ctx, p, cfg.targetModel, req)
	r.Scoring.Observe(p.Name(), time.Since(start), att.Err == nil)
	att.Kind = Classify(att.Err)
	attempts = append(attempts, att)
	if att.Err == nil {
		r.applyIsolation(p.Name(), att)
		r.recordQuota(p.Name(), resp)
		return resp, attempts, nil
	}
	r.applyIsolation(p.Name(), att)
	if r.Log != nil {
		r.Log.Warn("target dispatch failed",
			"provider", att.Provider, "model", att.Model, "kind", att.Kind, "err", att.Err)
	}
	return nil, attempts, &DispatchError{Attempts: attempts}
}
