// responses.go 承载 POST /v1/responses（遗留清单第 3 项，2026-08-29）：
// OpenAI Responses API 入站——Codex CLI（默认 wire_api=responses）与新
// 一代 OpenAI SDK 把本网关当官方端点直连。翻译走 internal/translate
// 纯函数对；策略指令、sticky 会话、护栏、记忆、缓存、审计全前置链照常。
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

// handleResponses 实现 Responses API 入站：归一化 → 分发 → 渲染。
func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if s.router == nil || len(s.router.Providers) == 0 {
		writeAPIError(w, http.StatusServiceUnavailable,
			"no upstream providers configured; set API keys or configure providers",
			"api_error", "")
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxChatRequestBody)
	raw, err := io.ReadAll(body)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "request body too large or unreadable",
			"invalid_request_error", "")
		return
	}
	var in translate.ResponsesRequest
	if err := json.Unmarshal(raw, &in); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(),
			"invalid_request_error", "")
		return
	}
	if in.Model == "" {
		writeAPIError(w, http.StatusBadRequest, "model is required", "invalid_request_error", "")
		return
	}
	if in.Input.Text == "" && len(in.Input.Items) == 0 {
		writeAPIError(w, http.StatusBadRequest, "input must not be empty", "invalid_request_error", "")
		return
	}

	req, degraded := translate.FromResponses(&in)
	if !s.applyGuardrails("/v1/responses", req, func(code int, msg string) {
		writeAPIError(w, code, msg, "invalid_request_error", "")
	}) {
		return
	}
	s.memoryRecall(w, r, req)
	opts, comboName, fusionReq, err := s.dispatchOptions(r, req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "")
		return
	}
	opts = append(opts, sessionOption(r)...)
	opts = append(opts, s.pinOption()...)
	if comboName != "" {
		opts = append(opts, s.comboCompress(r, req, comboName)...)
	}
	if fusionReq {
		s.handleFusion(w, r, req, "responses", req.Model, comboName, start,
			func(status int, msg string) {
				typ := "invalid_request_error"
				if status >= 500 {
					typ = "api_error"
				}
				writeAPIError(w, status, msg, typ, "")
			},
			func(resp *schema.Response) {
				setDegradedHeader(w, degraded)
				writeJSON(w, http.StatusOK, translate.ToResponses(resp))
			})
		return
	}

	if req.Stream {
		s.handleResponsesStream(w, r, req, opts, degraded,
			s.beginStreamAudit("responses", req.Model, comboName))
		return
	}

	if resp, ok := s.cache.Lookup(r.Context(), req); ok {
		w.Header().Set("X-OmniFusion-Cache", "hit")
		setDegradedHeader(w, degraded)
		writeJSON(w, http.StatusOK, translate.ToResponses(resp))
		s.auditDone("responses", req.Model, comboName, start, "cache", resp.Usage, true)
		return
	}
	resp, attempts, err := s.router.Dispatch(r.Context(), req, opts...)
	if err != nil {
		s.logDispatchFailure(req, attempts, err)
		writeAPIError(w, http.StatusBadGateway, upstreamErrorMessage(err), "api_error", "")
		s.auditFailed("responses", req.Model, comboName, start, err)
		return
	}
	w.Header().Set("X-OmniFusion-Cache", "miss")
	setDegradedHeader(w, mergeDegraded(degraded, attemptDegraded(attempts)))
	writeJSON(w, http.StatusOK, translate.ToResponses(resp))
	s.auditDone("responses", req.Model, comboName, start, resp.ProviderName, resp.Usage, false)
	go s.cache.WriteBack(context.WithoutCancel(r.Context()), req, resp)
	go s.memoryRecord(r, req, resp)
}

// handleResponsesStream 流式路径：buffer-first-chunk failover 在路由层
// 完成，这里把归一化 chunk 流编码为 Responses SSE 事件序列；断流也经
// 编码器 Finish 优雅收尾（response.completed 兜底）。
func (s *Server) handleResponsesStream(w http.ResponseWriter, r *http.Request,
	req *schema.UnifiedRequest, opts []routing.DispatchOption, degraded []string,
	audit *streamAudit) {
	stream, attempts, err := s.router.DispatchStream(r.Context(), req, opts...)
	if err != nil {
		s.logDispatchFailure(req, attempts, err)
		writeAPIError(w, http.StatusBadGateway, upstreamErrorMessage(err), "api_error", "")
		audit.finish(http.StatusBadGateway, "", dispatchErrKind(err))
		return
	}
	defer func() { _ = stream.Close() }()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError,
			"streaming unsupported by the underlying transport", "api_error", "")
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
	enc := translate.NewResponsesStreamEncoder()
	for {
		chunk, err := stream.Next(r.Context())
		if err == io.EOF {
			writeSSEFrames(w, flusher, enc.Finish())
			audit.finish(http.StatusOK, winner, "")
			return
		}
		if err != nil {
			s.log.Warn("upstream stream broken after first chunk; closing gracefully",
				"err", err)
			writeSSEFrames(w, flusher, enc.Finish())
			audit.finish(http.StatusOK, winner, "stream_broken")
			return
		}
		audit.firstChunk()
		audit.observe(chunk)
		writeSSEFrames(w, flusher, enc.Feed(chunk))
		if r.Context().Err() != nil {
			audit.finish(http.StatusOK, winner, "cancelled")
			return
		}
	}
}

// writeSSEFrames 逐帧写出并合并 Flush（TTFT 面每批一刷）。
func writeSSEFrames(w io.Writer, flusher http.Flusher, frames [][]byte) {
	for _, f := range frames {
		_, _ = w.Write(f)
	}
	flusher.Flush()
}
