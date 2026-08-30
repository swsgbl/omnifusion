// audit.go 是 审计+指标的单挂点：三协议数据面端点的每个出口
// （缓存命中/分发成功/分发失败/护栏拦截/流式收尾）调用一次 recordRequest，
// 同时驱动 Prometheus 指标（未装配零开销）与 request_log 落库（默认
// 开启；失败仅记日志，绝不影响请求面，超 max_rows 顺带裁最旧）。
package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/store"
)

// auditProviderNone 是未分发请求（护栏拦截等）的 provider 占位。
const auditProviderNone = "none"

// auditRecord 是一次数据面请求的结果面（双写共用口径）。
type auditRecord struct {
	Endpoint  string // chat|messages|gemini
	Model     string
	Provider  string // 赢家；缓存命中 "cache"；未分发 "none"
	Status    int
	TokensIn  int
	TokensOut int
	LatencyMS float64
	TTFTMS    float64 // <0 = 非流式或未到达首 chunk
	CacheHit  bool
	ErrKind   string // ErrorKind / guardrails / stream_broken / cancelled；成功空
	Combo     string
}

// recordRequest 双写指标与审计行（provider 空值归一为 none）。
func (s *Server) recordRequest(rec auditRecord) {
	if rec.Provider == "" {
		rec.Provider = auditProviderNone
	}
	d := time.Duration(rec.LatencyMS * float64(time.Millisecond))
	s.metrics.RecordRequest(rec.Endpoint, rec.Provider, rec.Status, d)
	s.metrics.RecordTokens(rec.Provider, rec.TokensIn, rec.TokensOut)
	if rec.TTFTMS >= 0 {
		s.metrics.RecordTTFT(rec.Endpoint, rec.Provider,
			time.Duration(rec.TTFTMS*float64(time.Millisecond)))
	}
	if !s.auditEnabled() {
		return
	}
	if err := s.st.InsertRequestLog(store.RequestLog{
		TS: time.Now().Unix(), Endpoint: rec.Endpoint, Model: rec.Model,
		Provider: rec.Provider, Status: rec.Status,
		TokensIn: rec.TokensIn, TokensOut: rec.TokensOut,
		LatencyMS: rec.LatencyMS, TTFTMS: rec.TTFTMS, CacheHit: rec.CacheHit,
		ErrKind: rec.ErrKind, Combo: rec.Combo,
	}); err != nil {
		s.log.Warn("audit: insert request_log", "err", err)
		return
	}
	if max := s.cfg.Audit.MaxRows; max > 0 {
		if _, err := s.st.PruneRequestLogs(max); err != nil {
			s.log.Warn("audit: prune request_log", "err", err)
		}
	}
}

// auditEnabled：配置未装配视为关闭（测试裸 Server 不落库）。
func (s *Server) auditEnabled() bool {
	return s.cfg != nil && s.cfg.Audit.Enabled && s.st != nil
}

// auditDone 是非流式成功出口（含缓存命中形态：provider="cache"）。
func (s *Server) auditDone(endpoint, model, combo string, start time.Time,
	provider string, u *schema.Usage, cacheHit bool) {
	rec := auditRecord{Endpoint: endpoint, Model: model, Provider: provider,
		Status: http.StatusOK, CacheHit: cacheHit, Combo: combo,
		LatencyMS: durMS(time.Since(start)), TTFTMS: -1} // -1 = 非流式
	if u != nil {
		rec.TokensIn, rec.TokensOut = u.PromptTokens, u.CompletionTokens
	}
	s.recordRequest(rec)
}

// auditFailed 是非流式失败出口（err 为分发聚合错误）。非流式分发的
// 失败一律 502 Bad Gateway（上游语义已并入错误体），不再收 status 参
// 数——unparam 审计确认全部调用点恒传 502（2026-08-29 CI 首验揪出）。
func (s *Server) auditFailed(endpoint, model, combo string, start time.Time, err error) {
	s.recordRequest(auditRecord{Endpoint: endpoint, Model: model, Combo: combo,
		Status: http.StatusBadGateway, LatencyMS: durMS(time.Since(start)), TTFTMS: -1,
		ErrKind: dispatchErrKind(err)})
}

// streamAudit 跟踪流式请求：TTFT 起点、流内 usage 聚合（尾帧覆盖）、
// 收尾出账一次（幂等——多条收尾路径并存时首到者生效）。
type streamAudit struct {
	srv     *Server
	rec     auditRecord
	start   time.Time
	firstAt time.Time
	done    bool
}

// beginStreamAudit 在端点入口开账（时延口径含翻译/护栏，与非流式一致）。
func (s *Server) beginStreamAudit(endpoint, model, combo string) *streamAudit {
	return &streamAudit{srv: s, start: time.Now(),
		rec: auditRecord{Endpoint: endpoint, Model: model, Combo: combo, TTFTMS: -1}}
}

// firstChunk 记首 chunk 时刻（幂等保最早）。
func (a *streamAudit) firstChunk() {
	if a == nil || !a.firstAt.IsZero() {
		return
	}
	a.firstAt = time.Now()
}

// observe 聚合流内 usage（上游尾帧口径，最后非空帧为准）。
func (a *streamAudit) observe(c *schema.Chunk) {
	if a == nil || c == nil || c.Usage == nil {
		return
	}
	a.rec.TokensIn = c.Usage.PromptTokens
	a.rec.TokensOut = c.Usage.CompletionTokens
}

// finish 出账（幂等）。status 200 + errKind 表"头已发出但流异常收尾"。
func (a *streamAudit) finish(status int, provider, errKind string) {
	if a == nil || a.done {
		return
	}
	a.done = true
	a.rec.Status, a.rec.Provider, a.rec.ErrKind = status, provider, errKind
	a.rec.LatencyMS = durMS(time.Since(a.start))
	if !a.firstAt.IsZero() {
		a.rec.TTFTMS = durMS(a.firstAt.Sub(a.start))
	}
	a.srv.recordRequest(a.rec)
}

// attemptWinner 取赢家 provider（末位成功尝试；全失败/无尝试空串）。
// Dispatch 与 DispatchStream 成功路径均以末位尝试收尾（routing 语义）。
func attemptWinner(attempts []routing.Attempt) string {
	if len(attempts) == 0 {
		return ""
	}
	last := attempts[len(attempts)-1]
	if last.Err != nil {
		return ""
	}
	return last.Provider
}

// dispatchErrKind 取聚合失败的末位归一类别（词表）。
func dispatchErrKind(err error) string {
	var de *routing.DispatchError
	if errors.As(err, &de) && len(de.Attempts) > 0 {
		return string(de.Attempts[len(de.Attempts)-1].Kind)
	}
	return "unknown"
}

// durMS 毫秒（微秒精度）。
func durMS(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}
