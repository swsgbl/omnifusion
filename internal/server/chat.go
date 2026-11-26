package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/obs"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/security"
	"github.com/swsgbl/omnifusion/internal/store"
)

// maxChatRequestBody 限制入站请求体（多模态 base64 场景留足余量）。
const maxChatRequestBody = 32 << 20 // 32 MiB

// handleChatCompletions 实现 POST /v1/chat/completions：
// stream=false 走 聚合路径；stream=true 走 SSE 路径
// （buffer-first-chunk failover 在路由层，见 routing.DispatchStream）。
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now() // 审计时延口径：端点入口→响应完成
	if s.router == nil || len(s.router.Providers) == 0 {
		writeAPIError(w, http.StatusServiceUnavailable,
			"no upstream providers configured; set API keys or configure providers",
			"server_error", "")
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxChatRequestBody)
	raw, err := io.ReadAll(body)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "request body too large or unreadable", "invalid_request_error", "")
		return
	}

	var req schema.UnifiedRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error", "")
		return
	}
	if req.Model == "" {
		writeAPIError(w, http.StatusBadRequest, "model is required", "invalid_request_error", "")
		return
	}
	if len(req.Messages) == 0 {
		writeAPIError(w, http.StatusBadRequest, "messages must not be empty", "invalid_request_error", "")
		return
	}
	// Guardrails：翻译完成后、路由分发前扫描正文（未装配零开销）。
	if !s.applyGuardrails("/v1/chat/completions", &req, func(code int, msg string) {
		writeAPIError(w, code, msg, "invalid_request_error", "")
	}) {
		return
	}
	// 会话记忆召回（opt-in 头）：命中注入 system 消息，永不阻断。
	s.memoryRecall(w, r, &req)
	opts, comboName, fusionReq, routeSrc, err := s.dispatchOptions(r, &req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "")
		return
	}
	opts = append(opts, sessionOption(r)...)
	opts = append(opts, s.pinOption()...) // 全局路由钉选
	if comboName != "" {
		opts = append(opts, s.comboCompress(r, &req, comboName)...)
	}
	if fusionReq { // @fusion：扇出合成短路（流式在其中 400）
		s.handleFusion(w, r, &req, "chat", req.Model, comboName, start,
			func(status int, msg string) {
				typ := "invalid_request_error"
				if status >= 500 {
					typ = "upstream_error"
				}
				writeAPIError(w, status, msg, typ, "")
			},
			func(resp *schema.Response) { writeJSON(w, http.StatusOK, resp) })
		return
	}
	if req.Stream {
		s.handleChatStream(w, r, &req, opts, s.beginStreamAudit("chat", req.Model, comboName))
		return
	}

	// L5 语义缓存查询：命中直接返回，不经压缩/路由/上游。
	// 证据面：cache-key 前缀随头返回（同前缀=同代际同请求形状）。
	if resp, ok := s.cache.Lookup(r.Context(), &req); ok {
		w.Header().Set("X-OmniFusion-Cache", "hit")
		w.Header().Set("X-OmniFusion-Cache-Key", s.cache.KeyPrefix(&req))
		writeJSON(w, http.StatusOK, resp)
		s.auditDone("chat", req.Model, comboName, start, "cache", resp.Usage, true)
		return
	}
	resp, attempts, err := s.router.Dispatch(r.Context(), &req, opts...)
	dec := routing.FoldDecision(attempts, err, req.Model, r.Header.Get("X-Request-Id"),
		routeSrc, time.Since(start).Milliseconds(), r.Context().Err() != nil)
	s.logRouteDecision("chat", dec)
	s.logGenAI(&obs.GenAICorrelation{
		Operation:      obs.OpChatCompletions,
		System:         dec.ChosenProvider,
		RequestModel:   req.Model,
		ResponseModel:  dec.ResolvedModel,
		RequestID:      r.Header.Get("X-Request-Id"),
		ConversationID: r.Header.Get(routing.HeaderSession),
	}, time.Since(start), err == nil)
	if err != nil {
		s.logDispatchFailure(&req, attempts, err)
		writeAPIError(w, http.StatusBadGateway, upstreamErrorMessage(err), "upstream_error", "")
		s.auditFailed("chat", req.Model, comboName, start, err)
		return
	}
	w.Header().Set("X-OmniFusion-Cache", "miss")
	w.Header().Set("X-OmniFusion-Cache-Key", s.cache.KeyPrefix(&req))
	w.Header().Set("X-OmniFusion-Route", dec.Summary())
	setDegradedHeader(w, attemptDegraded(attempts))
	s.setPremiumBudgetHeader(w, routeSrc)
	if resp.Usage != nil && s.costLedger != nil {
		if usd := s.costLedger.Estimate(resp.ProviderName, resp.Model,
			resp.Usage.PromptTokens, resp.Usage.CompletionTokens); usd > 0 {
			w.Header().Set("X-OmniFusion-Cost", formatCostUSD(usd))
		}
	}
	writeJSON(w, http.StatusOK, resp)
	s.auditDone("chat", req.Model, comboName, start, resp.ProviderName, resp.Usage, false)
	// L5 缓存异步回写：WithoutCancel 防客户端断开中断回写。
	go s.cache.WriteBack(context.WithoutCancel(r.Context()), &req, resp)
	// 会话记忆记录（opt-in 头）：非流式成功后旁路记录回合。
	go s.memoryRecord(r, &req, resp)
}

// logRouteDecision 把路由决策证据写成结构化日志行 + 回放记录落库
// （蓝图 Phase 9 replay record："能解释决策的最小证据集"——候选集/
// 原因码/耗时持久化，构造性不含用户载荷）。debug 级别防噪声；排障
// 时调 log.level=debug 即可回放。落库失败 warn 不阻断请求路径。
func (s *Server) logRouteDecision(endpoint string, dec *routing.RouteDecision) {
	if dec == nil {
		return
	}
	if s.log != nil {
		s.log.Debug("route decision",
			"model", dec.Model, "chosen", dec.ChosenProvider,
			"resolved_model", dec.ResolvedModel,
			"reasons", dec.ReasonCodes, "tries", dec.AttemptCount,
			"duration_ms", dec.DurationMS, "summary", dec.Summary())
	}
	if s.st == nil {
		return
	}
	evidence, err := json.Marshal(dec)
	if err != nil {
		evidence = []byte("{}")
	}
	if err := s.st.InsertRouteDecision(store.RouteDecisionRow{
		TS: time.Now().Unix(), Endpoint: endpoint, RequestID: dec.RequestID,
		Model: dec.Model, ChosenProvider: dec.ChosenProvider,
		Success: dec.Success, Tries: dec.AttemptCount,
		DurationMS: dec.DurationMS, Evidence: string(evidence),
	}); err != nil && s.log != nil {
		s.log.Warn("persist route decision failed", "err", err)
	}
}

// logGenAI 写 GenAI 语义约定关联行（蓝图 Phase 9）：OTel GenAI
// 命名的关联字段 + 耗时与结果——零遥测承诺下的本地结构化观测面。
// 字段集只含 ID/名称/枚举（载荷与 secret 永不进入）。
func (s *Server) logGenAI(c *obs.GenAICorrelation, d time.Duration, ok bool) {
	if s.log == nil {
		return
	}
	fields := append(c.Fields(), "duration_ms", d.Milliseconds(), "ok", ok)
	s.log.Info("genai operation", fields...)
}

// logDispatchFailure 把逐家尝试的失败原因写进日志（排障关键面），
// 值前缀 归一化错误类别（kind: err）。上游错误体可能回显密钥
// （恶意/故障上游）——出日志前一律过密钥形态脱敏（蓝图 Phase 8：
// secret 不得进入 logs）。
func (s *Server) logDispatchFailure(req *schema.UnifiedRequest, attempts []routing.Attempt, err error) {
	fields := make([]any, 0, len(attempts)*2+2)
	fields = append(fields, "model", req.Model)
	for _, a := range attempts {
		fields = append(fields, a.Provider, a.Kind.Label(security.RedactSecrets(errString(a.Err))))
		if a.Err != nil { // 逐 attempt 上游失败指标（赢家之前的轮空）
			s.metrics.RecordAttemptFailure(a.Provider, string(a.Kind))
		}
	}
	s.log.Error("chat completion failed", append(fields, "err", security.RedactSecrets(errString(err)))...)
}

// upstreamErrorMessage 是客户端可见的上游失败摘要。全链失败时先出
// 逐家可行动摘要（dispatchFailureSummary）；其余情况 UpstreamError 走
// 结构化摘要（不含体）；回退路径可能携带上游原文——脱敏后再出
// （纵深防御：即使未来新增回退路径，密钥形态也出不去）。
func upstreamErrorMessage(err error) string {
	var de *routing.DispatchError
	if errors.As(err, &de) && len(de.Attempts) > 0 {
		if summary := dispatchFailureSummary(de.Attempts); summary != "" {
			return summary
		}
		last := de.Attempts[len(de.Attempts)-1]
		if ue, ok := provider.IsUpstream(last.Err); ok {
			msg := fmt.Sprintf("upstream %s returned status %d", ue.Provider, ue.Status)
			if ue.Status == http.StatusForbidden {
				// 区域封锁（如 CN/HK 出口被 Groq 等拒）是最常见的 403 成因——
				// 给小白一句可行动的提示而不是裸状态码。
				msg += " (region-blocked? set HTTPS_PROXY to a permitted region, or pick another provider)"
			}
			return msg
		}
		return security.RedactSecrets(fmt.Sprintf("upstream %s failed: %v", last.Provider, last.Err))
	}
	return security.RedactSecrets(err.Error())
}

// failureSummaryMax 是摘要里逐家列出的上限：再多的失败折成一句计数，
// 摘要本身不该变成一堵墙。
const failureSummaryMax = 6

// kindHint 是错误类别 → 给用户的一句固定提示（不含任何上游原文）。
var kindHint = map[routing.ErrorKind]string{
	routing.KindAuthInvalid:    "check the API key",
	routing.KindQuotaExhausted: "quota or balance is exhausted",
	routing.KindModelGone:      "model was retired or is not available on this account",
	routing.KindRateLimit:      "rate limited, retry shortly",
	routing.KindNetOrTimeout:   "network unreachable or timed out",
	routing.KindUpstream5xx:    "upstream error",
	routing.KindStreamBroken:   "stream broke mid-response",
	routing.KindRequestError:   "request rejected",
}

// failureHint 是单条失败的提示：403 单独处理——区域封锁（如 CN/HK
// 出口被 Groq 等拒）是最常见的 403 成因，先给代理建议再提密钥。
func failureHint(kind routing.ErrorKind, status int) string {
	if kind == routing.KindAuthInvalid && status == http.StatusForbidden {
		return "region-blocked? set HTTPS_PROXY to a permitted region, or pick another provider; otherwise check the API key"
	}
	return kindHint[kind]
}

// dispatchFailureSummary 把全链失败压成一句可行动的摘要：逐个列出
// 真正打到上游的失败（provider/model: 状态码 类别 — 提示），被隔离/
// 超预算跳过的候选只报数量；一个上游都没打、全是跳过时直接点名谁被
// 隔离、到什么时候。2026-11-25 用户实撞：全部候选失败时只回"最后
// 一家 skipped (cooldown)"，看不出是哪把 key 失效、哪家欠费。
//
// 只含 provider、模型 id、状态码、类别与固定提示——上游响应体可能回显
// 密钥，一个字节都不带出去。没有任何失败记录时返回空串，由调用方
// 回落旧摘要。
func dispatchFailureSummary(attempts []routing.Attempt) string {
	var parts, skips []string
	seen := map[string]bool{}
	skipSeen := map[string]bool{}
	skipped := 0
	for _, a := range attempts {
		if a.Err == nil {
			continue
		}
		if reason, ok := skipReason(a); ok {
			skipped++
			target := a.Provider
			if a.Model != "" && strings.HasPrefix(reason, "model locked") {
				target += "/" + a.Model
			}
			if !skipSeen[target] && len(skips) < failureSummaryMax {
				skipSeen[target] = true
				skips = append(skips, target+" ("+reason+")")
			}
			continue
		}
		status := 0
		if ue, ok := provider.IsUpstream(a.Err); ok {
			status = ue.Status
		}
		// 同家同类同状态的失败只列一次（多模型逐个撞同一堵墙）。
		key := fmt.Sprintf("%s|%d|%s", a.Provider, status, a.Kind)
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(parts) >= failureSummaryMax {
			continue // 超出上限的只计数
		}

		target := a.Provider
		if a.Model != "" {
			target += "/" + a.Model
		}
		kind := string(a.Kind)
		if kind == "" {
			kind = "unknown"
		}
		part := fmt.Sprintf("%s: %s", target, kind)
		if status != 0 {
			part = fmt.Sprintf("%s: HTTP %d %s", target, status, kind)
		}
		if hint := failureHint(a.Kind, status); hint != "" {
			part += " (" + hint + ")"
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 && len(skips) == 0 {
		return ""
	}
	var b strings.Builder
	if len(parts) == 0 {
		fmt.Fprintf(&b, "no candidate could be tried: %s.", strings.Join(skips, "; "))
	} else {
		fmt.Fprintf(&b, "all candidates failed (%d distinct): %s", len(seen), strings.Join(parts, "; "))
		if rest := len(seen) - len(parts); rest > 0 {
			fmt.Fprintf(&b, "; (+%d more)", rest)
		}
		if skipped > 0 {
			fmt.Fprintf(&b, ". %d other candidate(s) were skipped because they are isolated or over budget", skipped)
		}
	}
	b.WriteString(" Isolation lifts by itself when the timer expires; fixing the provider (key / balance / network) in the dashboard clears it right away.")
	return security.RedactSecrets(b.String())
}

// skipReason 识别"没发请求就被跳过"的尝试并给出原因。provider 级隔离
// （冷却/熔断/配额窗口/链预算）走 SkipReason；模型级隔离在 tryOne 内
// 判定，错误文本是路由层自己拼的（不含任何上游原文）。
func skipReason(a routing.Attempt) (string, bool) {
	if a.SkipReason != "" {
		return a.SkipReason, true
	}
	if msg := a.Err.Error(); strings.HasPrefix(msg, "routing: model isolated (") {
		return strings.TrimSuffix(strings.TrimPrefix(msg, "routing: model isolated ("), ")"), true
	}
	return "", false
}

func errString(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

// apiError 是 OpenAI 风格的错误响应体。
type apiError struct {
	Error apiErrorBody `json:"error"`
}

type apiErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

func writeAPIError(w http.ResponseWriter, status int, msg, typ, code string) {
	writeJSON(w, status, apiError{Error: apiErrorBody{Message: msg, Type: typ, Code: code}})
}
