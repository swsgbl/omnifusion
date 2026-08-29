package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/routing"
)

// maxChatRequestBody 限制入站请求体（多模态 base64 场景留足余量）。
const maxChatRequestBody = 32 << 20 // 32 MiB

// handleChatCompletions 实现 POST /v1/chat/completions：
// stream=false 走 M1.4 聚合路径；stream=true 走 M1.5 SSE 路径
// （buffer-first-chunk failover 在路由层，见 routing.DispatchStream）。
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now() // M5.5 审计时延口径：端点入口→响应完成
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
	// M5.4 Guardrails：翻译完成后、路由分发前扫描正文（未装配零开销）。
	if !s.applyGuardrails("/v1/chat/completions", &req, func(code int, msg string) {
		writeAPIError(w, code, msg, "invalid_request_error", "")
	}) {
		return
	}
	// M6.4 会话记忆召回（opt-in 头）：命中注入 system 消息，永不阻断。
	s.memoryRecall(w, r, &req)
	opts, comboName, fusionReq, err := s.dispatchOptions(r, &req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "")
		return
	}
	opts = append(opts, sessionOption(r)...)
	opts = append(opts, s.pinOption()...) // M5.2 全局路由钉选
	if comboName != "" {
		opts = append(opts, s.comboCompress(r, &req, comboName)...)
	}
	if fusionReq { // M6.1 @fusion：扇出合成短路（流式在其中 400）
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

	// L5 语义缓存查询（M4.6）：命中直接返回，不经压缩/路由/上游。
	if resp, ok := s.cache.Lookup(r.Context(), &req); ok {
		w.Header().Set("X-OmniFusion-Cache", "hit")
		writeJSON(w, http.StatusOK, resp)
		s.auditDone("chat", req.Model, comboName, start, "cache", resp.Usage, true)
		return
	}
	resp, attempts, err := s.router.Dispatch(r.Context(), &req, opts...)
	if err != nil {
		s.logDispatchFailure(&req, attempts, err)
		writeAPIError(w, http.StatusBadGateway, upstreamErrorMessage(err), "upstream_error", "")
		s.auditFailed("chat", req.Model, comboName, start, err)
		return
	}
	w.Header().Set("X-OmniFusion-Cache", "miss")
	setDegradedHeader(w, attemptDegraded(attempts))
	writeJSON(w, http.StatusOK, resp)
	s.auditDone("chat", req.Model, comboName, start, resp.ProviderName, resp.Usage, false)
	// L5 缓存异步回写：WithoutCancel 防客户端断开中断回写（M4.6）。
	go s.cache.WriteBack(context.WithoutCancel(r.Context()), &req, resp)
	// M6.4 会话记忆记录（opt-in 头）：非流式成功后旁路记录回合。
	go s.memoryRecord(r, &req, resp)
}

// logDispatchFailure 把逐家尝试的失败原因写进日志（排障关键面），
// 值前缀 M2.1 归一化错误类别（kind: err）。
func (s *Server) logDispatchFailure(req *schema.UnifiedRequest, attempts []routing.Attempt, err error) {
	fields := make([]any, 0, len(attempts)*2+2)
	fields = append(fields, "model", req.Model)
	for _, a := range attempts {
		fields = append(fields, a.Provider, a.Kind.Label(errString(a.Err)))
		if a.Err != nil { // M5.5：逐 attempt 上游失败指标（赢家之前的轮空）
			s.metrics.RecordAttemptFailure(a.Provider, string(a.Kind))
		}
	}
	s.log.Error("chat completion failed", append(fields, "err", err)...)
}

func upstreamErrorMessage(err error) string {
	var de *routing.DispatchError
	if errors.As(err, &de) && len(de.Attempts) > 0 {
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
		return fmt.Sprintf("upstream %s failed: %v", last.Provider, last.Err)
	}
	return err.Error()
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
