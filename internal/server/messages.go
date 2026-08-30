// messages.go 承载 POST /v1/messages：Anthropic Messages 协议
// 入站，Claude Code 把本网关当 Anthropic 端点直连（ANTHROPIC_BASE_URL +
// ANTHROPIC_API_KEY/AUTH_TOKEN），无需改动其配置协议。翻译走
// internal/translate 纯函数对；策略指令与 sticky 会话头照常生效。
package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/translate"
)

// handleMessages 实现 Anthropic Messages 入站：归一化 → 分发 → 渲染。
func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	start := time.Now() // 审计时延口径
	if s.router == nil || len(s.router.Providers) == 0 {
		writeAnthropicError(w, http.StatusServiceUnavailable, "api_error",
			"no upstream providers configured; set API keys or configure providers")
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxChatRequestBody)
	raw, err := io.ReadAll(body)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error",
			"request body too large or unreadable")
		return
	}
	var in translate.AnthropicRequest
	if err := json.Unmarshal(raw, &in); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error",
			"invalid JSON body: "+err.Error())
		return
	}
	if in.Model == "" {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	if len(in.Messages) == 0 {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "messages must not be empty")
		return
	}
	if in.MaxTokens <= 0 {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "max_tokens is required and must be positive")
		return
	}

	req, degraded := translate.FromAnthropicMessages(&in)
	// Guardrails：翻译后、分发前扫描正文（未装配零开销）。
	if !s.applyGuardrails("/v1/messages", req, func(code int, msg string) {
		writeAnthropicError(w, code, "invalid_request_error", msg)
	}) {
		return
	}
	// 会话记忆召回（opt-in 头）：命中注入 system 消息，永不阻断。
	s.memoryRecall(w, r, req)
	opts, comboName, fusionReq, err := s.dispatchOptions(r, req) // @fast:model 指令与策略头同样可用
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	opts = append(opts, sessionOption(r)...)
	opts = append(opts, s.pinOption()...) // 全局路由钉选
	if comboName != "" {
		opts = append(opts, s.comboCompress(r, req, comboName)...)
	}
	if fusionReq { // @fusion：扇出合成短路（流式在其中 400）
		s.handleFusion(w, r, req, "messages", req.Model, comboName, start,
			func(status int, msg string) {
				typ := "invalid_request_error"
				if status >= 500 {
					typ = "api_error"
				}
				writeAnthropicError(w, status, typ, msg)
			},
			func(resp *schema.Response) {
				setDegradedHeader(w, degraded)
				writeJSON(w, http.StatusOK, translate.ToAnthropicMessages(resp))
			})
		return
	}

	if req.Stream {
		s.handleMessagesStream(w, r, req, opts, degraded,
			s.beginStreamAudit("messages", req.Model, comboName))
		return
	}

	// L5 语义缓存查询：键取自 IR，跨协议与 /v1/chat/completions
	// 共享命中；翻译期降级标记由入站请求形状决定，命中时照常给出。
	if resp, ok := s.cache.Lookup(r.Context(), req); ok {
		w.Header().Set("X-OmniFusion-Cache", "hit")
		setDegradedHeader(w, degraded)
		writeJSON(w, http.StatusOK, translate.ToAnthropicMessages(resp))
		s.auditDone("messages", req.Model, comboName, start, "cache", resp.Usage, true)
		return
	}
	resp, attempts, err := s.router.Dispatch(r.Context(), req, opts...)
	if err != nil {
		s.logDispatchFailure(req, attempts, err)
		writeAnthropicError(w, http.StatusBadGateway, "api_error", upstreamErrorMessage(err))
		s.auditFailed("messages", req.Model, comboName, start, err)
		return
	}
	w.Header().Set("X-OmniFusion-Cache", "miss")
	setDegradedHeader(w, mergeDegraded(degraded, attemptDegraded(attempts)))
	writeJSON(w, http.StatusOK, translate.ToAnthropicMessages(resp))
	s.auditDone("messages", req.Model, comboName, start, resp.ProviderName, resp.Usage, false)
	// L5 缓存异步回写：WithoutCancel 防客户端断开中断回写。
	go s.cache.WriteBack(context.WithoutCancel(r.Context()), req, resp)
	// 会话记忆记录（opt-in 头）：非流式成功后旁路记录回合。
	go s.memoryRecord(r, req, resp)
}

// handleMessagesStream 流式路径：buffer-first-chunk failover 已在路由层
// 完成，这里把归一化 chunk 流实时编码为 Anthropic SSE 事件序列；断流
// 也经编码器 Finish 优雅收尾（精神的入站侧落地）。
func (s *Server) handleMessagesStream(w http.ResponseWriter, r *http.Request,
	req *schema.UnifiedRequest, opts []routing.DispatchOption, degraded []string,
	audit *streamAudit) {
	stream, attempts, err := s.router.DispatchStream(r.Context(), req, opts...)
	if err != nil {
		s.logDispatchFailure(req, attempts, err)
		writeAnthropicError(w, http.StatusBadGateway, "api_error", upstreamErrorMessage(err))
		audit.finish(http.StatusBadGateway, "", dispatchErrKind(err))
		return
	}
	defer func() { _ = stream.Close() }()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error",
			"streaming unsupported by the underlying transport")
		return
	}
	setDegradedHeader(w, mergeDegraded(degraded, attemptDegraded(attempts)))
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	winner := attemptWinner(attempts)
	enc := translate.NewAnthropicStreamEncoder()
	for {
		chunk, err := stream.Next(r.Context())
		if err == io.EOF {
			writeAnthropicFrames(w, flusher, enc.Finish())
			audit.finish(http.StatusOK, winner, "")
			return
		}
		if err != nil {
			s.log.Warn("upstream stream broken after first chunk; closing gracefully",
				"err", err)
			writeAnthropicFrames(w, flusher, enc.Finish())
			audit.finish(http.StatusOK, winner, "stream_broken")
			return
		}
		audit.firstChunk()
		audit.observe(chunk)
		writeAnthropicFrames(w, flusher, enc.Feed(chunk))
		if r.Context().Err() != nil {
			audit.finish(http.StatusOK, winner, "cancelled")
			return // 客户端已断开
		}
	}
}

// writeAnthropicFrames 逐帧写出并合并 Flush（TTFT 面每批一刷）。
func writeAnthropicFrames(w io.Writer, flusher http.Flusher, frames [][]byte) {
	for _, f := range frames {
		_, _ = w.Write(f)
	}
	flusher.Flush()
}

// writeAnthropicError 写 Anthropic 形错误体（{"type":"error",...}）。
func writeAnthropicError(w http.ResponseWriter, code int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": typ, "message": msg},
	})
}
