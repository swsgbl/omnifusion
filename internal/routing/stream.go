package routing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
)

// streamTimeout 约束单条流的整体寿命（ item 4：流 300s）。
const streamTimeout = 300 * time.Second

// ErrStreamUnsupported 记录候选不支持 stream=true 的跳过原因。
var ErrStreamUnsupported = errors.New("routing: provider does not support streaming")

// DispatchStream 按固定顺序尝试候选，执行 buffer-first-chunk 语义
// （ item12 / ）：
// - 首 chunk 落地前：连接失败、非 2xx、空体/坏体都允许切换下一家；
// - 首 chunk 落地后：返回的事件流已不可撤回，中途断流由调用方记日志，
// 不再切换（"cannot un-ship bytes"）。
//
// 成功返回的事件流第一个 Next 必定回放缓冲的首 chunk。
// opts 可逐请求覆盖策略（WithStrategyName，）。
func (r *Router) DispatchStream(ctx context.Context, req *schema.UnifiedRequest, opts ...DispatchOption) (provider.ChunkStream, []Attempt, error) {
	if len(r.Providers) == 0 {
		return nil, nil, errors.New("routing: no providers configured")
	}
	cfg := resolveOptions(opts)
	cands, err := r.candidatesFor(cfg, req) // smart 指令在此分流到 ML 计划
	if err != nil {
		return nil, nil, err
	}
	attempts := make([]Attempt, 0, len(r.Providers))
	chainStart := time.Now()
	hasSafetyLane := chainHasSafety(cands)
	for i, c := range cands {
		if ctx.Err() != nil {
			break
		}
		if att, skipped := r.skipIfBlocked(c.p); skipped {
			attempts = append(attempts, att)
			continue
		}
		if att, skipped := r.skipIfChainOverBudget(c, cands[i:], chainStart); skipped {
			attempts = append(attempts, att)
			continue
		}
		sp, ok := provider.AsStreamParser(c.p)
		if !ok {
			attempts = append(attempts, Attempt{
				Provider: c.p.Name(),
				Err:      ErrStreamUnsupported,
				Kind:     Classify(ErrStreamUnsupported), // unknown：不支持不是上游的错
			})
			continue
		}
		// 单次尝试帽只约束"到首 chunk"的窗口（死连接的静默体不再吃满
		// 300s 流超时）；首 chunk 落地后流寿命由 streamTimeout 治理。
		attemptBudget := time.Duration(0)
		if hasSafetyLane && !c.safety {
			attemptBudget = failoverAttemptBudget
		}
		start := time.Now()
		stream, att := r.tryOneStream(ctx, c.p, sp, c.model, req, attemptBudget)
		// 延迟口径：到首 chunk 落地（可切换窗口的边界）为止。
		r.Scoring.Observe(c.p.Name(), time.Since(start), att.Err == nil)
		att.Kind = Classify(att.Err)
		attempts = append(attempts, att)
		if att.Err == nil {
			r.applyIsolation(c.p.Name(), att)
			if r.Quota != nil {
				r.Quota.RecordRequest(c.p.Name()) // token 用量由流 Close 时补记
			}
			r.bindSession(cfg.sessionID, c.p.Name())
			return stream, attempts, nil
		}
		r.applyIsolation(c.p.Name(), att)
		r.observeLedger(att)
		if r.Log != nil {
			r.Log.Warn("stream provider attempt failed",
				"provider", att.Provider, "model", att.Model, "kind", att.Kind, "err", att.Err)
		}
	}
	return nil, attempts, &DispatchError{Attempts: attempts}
}

// tryOneStream 执行单次流尝试。budget>0 时用 watchdog 定时器罩住
// "连接 → 解析 → 首 chunk"整窗（到点 cancel 本跳的 sctx——Do/体读/
// 嗅探 Peek 全部随之解除阻塞）；首 chunk 落地即停表，流寿命仍由
// streamTimeout 约束——预算不掐断长思考流。
func (r *Router) tryOneStream(ctx context.Context, p provider.Provider, sp provider.StreamParser, model string, req *schema.UnifiedRequest, budget time.Duration) (provider.ChunkStream, Attempt) {
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

	sctx, cancel := context.WithTimeout(ctx, streamTimeout)
	// 预算 watchdog：到点 cancel sctx，本跳任何阻塞阶段（含 ParseStream
	// 的体形嗅探 Peek）一并解除；成功路径在返回前 Stop。
	var watchdog *time.Timer
	if budget > 0 {
		watchdog = time.AfterFunc(budget, cancel)
	}

	fail := func(err error) (provider.ChunkStream, Attempt) {
		if watchdog != nil {
			watchdog.Stop()
		}
		cancel()
		att.Err = err
		return nil, att
	}

	httpReq, err := http.NewRequestWithContext(sctx, call.Method, call.URL, bytes.NewReader(call.Body))
	if err != nil {
		return fail(fmt.Errorf("build request: %w", err))
	}
	httpReq.Header = call.Header

	upstreamResp, err := sp.StreamClient().Do(httpReq)
	if err != nil {
		return fail(fmt.Errorf("upstream transport: %w", err))
	}

	events, err := sp.ParseStream(sctx, call, upstreamResp)
	if err != nil {
		_ = events.Close()
		return fail(err)
	}

	// buffer-first-chunk：先取出首个事件，失败还能换家（watchdog 仍在
	// 罩这一窗）。sctx 被 watchdog 掐断时 Next 以 ctx 错误返回。
	first, err := events.Next(sctx)
	if err != nil {
		_ = events.Close()
		if sctx.Err() != nil && budget > 0 {
			return fail(errFirstChunkBudget)
		}
		if errors.Is(err, io.EOF) { // 空流等价于无输出
			return fail(fmt.Errorf("stream produced no chunks"))
		}
		return fail(fmt.Errorf("stream first chunk: %w", err))
	}
	if watchdog != nil {
		watchdog.Stop() // 首 chunk 已落地：预算使命完成，长流交给 streamTimeout
	}
	return &bufferedStream{
		first:    first,
		inner:    events,
		cancel:   cancel,
		quota:    r.Quota,
		provider: p.Name(),
		onBreak:  r.midStreamReporter(p.Name(), call.Model),
	}, att
}

// errFirstChunkBudget 是首 chunk 预算超时（可换家的本地判定）。
var errFirstChunkBudget = errors.New("stream first chunk budget exceeded")

// midStreamReporter 产出 bufferedStream 的流中断裂回调（
// un-ship bytes"——已发出的流不可撤回，
// 不切换，只记日志 + 健康降分 + 隔离回报；stream_broken 计入熔断
// 窗口，反复断流的家最终会被熔断拦下）。
func (r *Router) midStreamReporter(name, model string) func(error) {
	return func(err error) {
		if r.Scoring != nil {
			r.Scoring.ObserveFailure(name)
		}
		kind := Classify(err)
		r.applyIsolation(name, Attempt{Model: model, Err: err, Kind: kind})
		if r.Log != nil {
			r.Log.Warn("mid-stream break after first chunk; health penalty applied",
				"provider", name, "model", model, "kind", kind, "err", err)
		}
	}
}

// bufferedStream 回放缓冲的首 chunk 后透传内层事件流；Close 同时释放
// 流级 context，并把流中见到的 usage（取最大值，防累计型/终值型差异）
// 补记进配额 token 窗口。
type bufferedStream struct {
	first    *schema.Chunk
	inner    provider.ChunkStream
	cancel   context.CancelFunc
	quota    *QuotaTracker
	provider string
	tokens   int64
	onBreak  func(error) // 流中断裂回调；nil 表示不回报
	reported bool        // 回调只触发一次
}

// Next implements provider.ChunkStream.
func (b *bufferedStream) Next(ctx context.Context) (*schema.Chunk, error) {
	if b.first != nil {
		c := b.first
		b.first = nil
		return c, nil
	}
	c, err := b.inner.Next(ctx)
	if err == nil {
		if c != nil && c.Usage != nil && int64(c.Usage.TotalTokens) > b.tokens {
			b.tokens = int64(c.Usage.TotalTokens)
		}
		return c, nil
	}
	// 流中断裂（非 EOF）：触发一次健康降分回报。调用方 ctx 已取消
	// （客户端断开）时跳过——那不是上游的病。
	if !errors.Is(err, io.EOF) && !b.reported && ctx.Err() == nil && b.onBreak != nil {
		b.reported = true
		b.onBreak(err)
	}
	return c, err
}

// Close implements provider.ChunkStream.
func (b *bufferedStream) Close() error {
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	if b.quota != nil && b.tokens > 0 {
		b.quota.RecordTokens(b.provider, b.tokens)
	}
	return b.inner.Close()
}
