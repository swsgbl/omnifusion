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
	cands := r.candidatesFor(cfg, req) // smart 指令在此分流到 ML 计划
	attempts := make([]Attempt, 0, len(r.Providers))
	for _, c := range cands {
		if ctx.Err() != nil {
			break
		}
		if att, skipped := r.skipIfBlocked(c.p); skipped {
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
		start := time.Now()
		stream, att := r.tryOneStream(ctx, c.p, sp, c.model, req)
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
		if r.Log != nil {
			r.Log.Warn("stream provider attempt failed",
				"provider", att.Provider, "model", att.Model, "kind", att.Kind, "err", att.Err)
		}
	}
	return nil, attempts, &DispatchError{Attempts: attempts}
}

func (r *Router) tryOneStream(ctx context.Context, p provider.Provider, sp provider.StreamParser, model string, req *schema.UnifiedRequest) (provider.ChunkStream, Attempt) {
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
	httpReq, err := http.NewRequestWithContext(sctx, call.Method, call.URL, bytes.NewReader(call.Body))
	if err != nil {
		cancel()
		att.Err = fmt.Errorf("build request: %w", err)
		return nil, att
	}
	httpReq.Header = call.Header

	upstreamResp, err := sp.StreamClient().Do(httpReq)
	if err != nil {
		cancel()
		att.Err = fmt.Errorf("upstream transport: %w", err)
		return nil, att
	}

	events, err := sp.ParseStream(sctx, call, upstreamResp)
	if err != nil {
		cancel()
		att.Err = err
		return nil, att
	}

	// buffer-first-chunk：先取出首个事件，失败还能换家。
	first, err := events.Next(sctx)
	if err != nil {
		_ = events.Close()
		cancel()
		if errors.Is(err, io.EOF) { // 空流等价于无输出
			att.Err = fmt.Errorf("stream produced no chunks")
		} else {
			att.Err = fmt.Errorf("stream first chunk: %w", err)
		}
		return nil, att
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
