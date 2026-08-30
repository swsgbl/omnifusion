// gemini.go 承载 POST /v1beta/models/{model}:generateContent：
// Gemini generateContent 协议入站，Google SDK / aistudio.google.com 分享
// 出的代码把本网关当 Gemini 端点直连。Go mux 的通配符后不能跟字面
// 后缀（"{model}:generateContent" 不可声明），故整段前缀注册后在
// handler 内手动拆 model 与 action。翻译走 internal/translate 纯函数
// 对；策略指令与 sticky 会话头照常生效。
package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/translate"
)

// geminiPathPrefix 是入站路径前缀（与 server.go 的注册一致）。
const geminiPathPrefix = "/v1beta/models/"

// splitGeminiPath 拆出 model 与 action：按最后一个 ":" 分界（model 侧
// 经 PathEscape 后 ":" 不受影响），action 仅接受 generateContent /
// streamGenerateContent，否则按未知路由处理。
func splitGeminiPath(p string) (model, action string, ok bool) {
	rest := p[len(geminiPathPrefix):]
	pos := lastColon(rest)
	if pos <= 0 {
		return "", "", false
	}
	model, action = rest[:pos], rest[pos+1:]
	if action != "generateContent" && action != "streamGenerateContent" {
		return "", "", false
	}
	if m, err := url.PathUnescape(model); err == nil {
		model = m
	}
	return model, action, true
}

// lastColon 找最后一个 ":"（action 分界）。
func lastColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}

// handleGeminiGenerateContent 实现 Gemini 入站：拆路径 → 归一化 →
// 分发 → 渲染（model 取自路径、stream 由 action 决定）。
func (s *Server) handleGeminiGenerateContent(w http.ResponseWriter, r *http.Request) {
	start := time.Now() // 审计时延口径
	if s.router == nil || len(s.router.Providers) == 0 {
		writeGeminiError(w, http.StatusServiceUnavailable, "UNAVAILABLE",
			"no upstream providers configured; set API keys or configure providers")
		return
	}
	model, action, ok := splitGeminiPath(r.URL.Path)
	if !ok || model == "" {
		writeGeminiError(w, http.StatusNotFound, "NOT_FOUND",
			"expected /v1beta/models/<model>:generateContent or :streamGenerateContent")
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxChatRequestBody)
	raw, err := io.ReadAll(body)
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"request body too large or unreadable")
		return
	}
	var in translate.GeminiRequest
	if err := json.Unmarshal(raw, &in); err != nil {
		writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"invalid JSON body: "+err.Error())
		return
	}

	stream := action == "streamGenerateContent"
	req, degraded := translate.FromGeminiGenerateContent(model, &in, stream)
	// Guardrails：翻译后、分发前扫描正文（未装配零开销）。
	if !s.applyGuardrails(r.URL.Path, req, func(code int, msg string) {
		writeGeminiError(w, code, "INVALID_ARGUMENT", msg)
	}) {
		return
	}
	// 会话记忆召回（opt-in 头）：命中注入 system 消息，永不阻断。
	s.memoryRecall(w, r, req)
	opts, comboName, fusionReq, err := s.dispatchOptions(r, req) // @fast:model 指令与策略头同样可用
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	opts = append(opts, sessionOption(r)...)
	opts = append(opts, s.pinOption()...) // 全局路由钉选
	if comboName != "" {
		opts = append(opts, s.comboCompress(r, req, comboName)...)
	}
	if fusionReq { // @fusion：扇出合成短路（流式在其中 400）
		s.handleFusion(w, r, req, "gemini", model, comboName, start,
			func(status int, msg string) {
				code := "INVALID_ARGUMENT"
				if status >= 500 {
					code = "UNAVAILABLE"
				}
				writeGeminiError(w, status, code, msg)
			},
			func(resp *schema.Response) {
				setDegradedHeader(w, degraded)
				writeJSON(w, http.StatusOK, translate.ToGeminiGenerateContent(resp))
			})
		return
	}

	if req.Stream {
		s.handleGeminiStream(w, r, req, opts, degraded,
			s.beginStreamAudit("gemini", req.Model, comboName))
		return
	}

	// L5 语义缓存查询：键取自 IR，跨协议与另两端点共享命中；
	// 翻译期降级标记由入站请求形状决定，命中时照常给出。
	if resp, ok := s.cache.Lookup(r.Context(), req); ok {
		w.Header().Set("X-OmniFusion-Cache", "hit")
		setDegradedHeader(w, degraded)
		writeJSON(w, http.StatusOK, translate.ToGeminiGenerateContent(resp))
		s.auditDone("gemini", model, comboName, start, "cache", resp.Usage, true)
		return
	}
	resp, attempts, err := s.router.Dispatch(r.Context(), req, opts...)
	if err != nil {
		s.logDispatchFailure(req, attempts, err)
		writeGeminiError(w, http.StatusBadGateway, "UNAVAILABLE", upstreamErrorMessage(err))
		s.auditFailed("gemini", model, comboName, start, err)
		return
	}
	w.Header().Set("X-OmniFusion-Cache", "miss")
	setDegradedHeader(w, mergeDegraded(degraded, attemptDegraded(attempts)))
	writeJSON(w, http.StatusOK, translate.ToGeminiGenerateContent(resp))
	s.auditDone("gemini", model, comboName, start, resp.ProviderName, resp.Usage, false)
	// L5 缓存异步回写：WithoutCancel 防客户端断开中断回写。
	go s.cache.WriteBack(context.WithoutCancel(r.Context()), req, resp)
	// 会话记忆记录（opt-in 头）：非流式成功后旁路记录回合。
	go s.memoryRecord(r, req, resp)
}

// handleGeminiStream 流式路径：buffer-first-chunk failover 已在路由层
// 完成，这里把归一化 chunk 流编码为 Gemini SSE 帧；断流也经编码器
// Finish 优雅收尾。
func (s *Server) handleGeminiStream(w http.ResponseWriter, r *http.Request,
	req *schema.UnifiedRequest, opts []routing.DispatchOption, degraded []string,
	audit *streamAudit) {
	stream, attempts, err := s.router.DispatchStream(r.Context(), req, opts...)
	if err != nil {
		s.logDispatchFailure(req, attempts, err)
		writeGeminiError(w, http.StatusBadGateway, "UNAVAILABLE", upstreamErrorMessage(err))
		audit.finish(http.StatusBadGateway, "", dispatchErrKind(err))
		return
	}
	defer func() { _ = stream.Close() }()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeGeminiError(w, http.StatusInternalServerError, "INTERNAL",
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
	enc := translate.NewGeminiStreamEncoder()
	for {
		chunk, err := stream.Next(r.Context())
		if err == io.EOF {
			writeGeminiFrames(w, flusher, enc.Finish())
			audit.finish(http.StatusOK, winner, "")
			return
		}
		if err != nil {
			s.log.Warn("upstream stream broken after first chunk; closing gracefully",
				"err", err)
			writeGeminiFrames(w, flusher, enc.Finish())
			audit.finish(http.StatusOK, winner, "stream_broken")
			return
		}
		audit.firstChunk()
		audit.observe(chunk)
		writeGeminiFrames(w, flusher, enc.Feed(chunk))
		if r.Context().Err() != nil {
			audit.finish(http.StatusOK, winner, "cancelled")
			return // 客户端已断开
		}
	}
}

// setDegradedHeader 打显式降级标记。
func setDegradedHeader(w http.ResponseWriter, degraded []string) {
	if len(degraded) > 0 {
		w.Header().Set("X-OmniFusion-Degraded", strings.Join(degraded, ", "))
	}
}

// writeGeminiFrames 逐帧写出并合并 Flush。
func writeGeminiFrames(w io.Writer, flusher http.Flusher, frames [][]byte) {
	for _, f := range frames {
		_, _ = w.Write(f)
	}
	flusher.Flush()
}

// writeGeminiError 写 Google 形错误体（{"error":{code,message,status}}）。
func writeGeminiError(w http.ResponseWriter, code int, status, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"code": code, "message": msg, "status": status},
	})
}
