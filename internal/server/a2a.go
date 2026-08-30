// a2a.go 是 A2A v1.0 协议的 HTTP 边界：AgentCard 发现端点
// （公开）+ JSON-RPC 2.0 /rpc（网关 key 鉴权）。网关以「无状态代理
// agent」形态接入：SendMessage 走 Message-only，流式走任务生命周期流
// （transient task，事后不可查询）。
package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/swsgbl/omnifusion/internal/a2a"
	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/routing"
)

// SetA2A 注入 A2A AgentCard 与缺省目标模型。未装配（nil card）
// 时不注册 /.well-known/agent-card.json 与 /rpc 路由。
func (s *Server) SetA2A(card *a2a.AgentCard, defaultModel string) {
	s.a2aCard = card
	s.a2aModel = defaultModel
}

// handleA2ACard 输出发现清单（无敏感信息：公开端点，业界惯例）。
func (s *Server) handleA2ACard(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.a2aCard)
}

// handleA2ARPC 实现 POST /rpc：JSON-RPC 2.0 信封 + A2A 方法分发。
func (s *Server) handleA2ARPC(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body := http.MaxBytesReader(w, r.Body, maxChatRequestBody)
	raw, err := io.ReadAll(body)
	if err != nil {
		s.writeA2AError(w, nil, a2a.CodeInvalidRequest, "request body too large or unreadable")
		return
	}
	var req a2a.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		s.writeA2AError(w, nil, a2a.CodeParse, "invalid JSON: "+err.Error())
		return
	}
	if req.JSONRPC != "2.0" {
		s.writeA2AError(w, req.ID, a2a.CodeInvalidRequest, `jsonrpc must be "2.0"`)
		return
	}
	if len(req.ID) == 0 { // JSON-RPC notification：不产生响应
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch req.Method {
	case "SendMessage":
		s.a2aSend(w, r, &req, start)
	case "SendStreamingMessage":
		s.a2aStream(w, r, &req, start)
	case "GetTask", "CancelTask", "SubscribeToTask":
		s.writeA2AError(w, req.ID, a2a.CodeTaskNotFound,
			"gateway agent is stateless; tasks are transient (stream-only)")
	case "ListTasks":
		s.writeA2AError(w, req.ID, a2a.CodeUnsupportedOperation,
			"task listing is not supported (stateless gateway agent)")
	default:
		s.writeA2AError(w, req.ID, a2a.CodeMethodNotFound, "unknown method "+req.Method)
	}
}

// a2aPrepare 完成 SendMessage/流式共用的前置：参数解码 → IR 翻译 →
// 护栏 → 路由选项（策略/组合/会话亲和/钉选）→ 组合压缩绑定。
// fusion 请求返回 fusionReq=true（调用方分流到 handleFusion）。
func (s *Server) a2aPrepare(w http.ResponseWriter, r *http.Request, req *a2a.Request) (
	ureq *schema.UnifiedRequest, opts []routing.DispatchOption, comboName string, fusionReq bool, ctxID string, ok bool) {
	var params a2a.SendMessageParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			s.writeA2AError(w, req.ID, a2a.CodeInvalidParams, "params: "+err.Error())
			return nil, nil, "", false, "", false
		}
	}
	ureq, err := a2a.ToUnified(&params.Message, s.a2aModel)
	if err != nil {
		code := a2a.CodeInvalidParams
		if err == a2a.ErrNoContent {
			code = a2a.CodeContentTypeNotSupport
		}
		s.writeA2AError(w, req.ID, code, err.Error())
		return nil, nil, "", false, "", false
	}
	if ureq.Model == "" {
		s.writeA2AError(w, req.ID, a2a.CodeInvalidParams,
			"no target model: set message.metadata.model or a2a.default_model in the gateway config")
		return nil, nil, "", false, "", false
	}
	if !s.applyGuardrails("/rpc", ureq, func(code int, msg string) {
		s.writeA2AError(w, req.ID, a2a.CodeInvalidParams, "guardrails: "+msg)
	}) {
		return nil, nil, "", false, "", false
	}
	opts, comboName, fusionReq, err = s.dispatchOptions(r, ureq)
	if err != nil {
		s.writeA2AError(w, req.ID, a2a.CodeInvalidParams, err.Error())
		return nil, nil, "", false, "", false
	}
	if fusionReq {
		return ureq, nil, comboName, true, params.Message.ContextID, true
	}
	if params.Message.ContextID != "" { // A2A contextId → 会话亲和（sticky）
		opts = append(opts, routing.WithSession(params.Message.ContextID))
	}
	opts = append(opts, s.pinOption()...)
	if comboName != "" {
		opts = append(opts, s.comboCompress(r, ureq, comboName)...)
	}
	return ureq, opts, comboName, false, params.Message.ContextID, true
}

// a2aSend 处理非流式 SendMessage：Message-only 响应（简单交互不建任务）。
func (s *Server) a2aSend(w http.ResponseWriter, r *http.Request, req *a2a.Request, start time.Time) {
	ureq, opts, comboName, fusionReq, _, ok := s.a2aPrepare(w, r, req)
	if !ok {
		return
	}
	if fusionReq { // @fusion：批合成，协议形态由回调注入
		s.handleFusion(w, r, ureq, "a2a", ureq.Model, comboName, start,
			func(status int, m string) {
				s.writeA2AError(w, req.ID, a2a.CodeInternal, m)
			},
			func(resp *schema.Response) {
				writeJSON(w, http.StatusOK, a2a.Response{
					JSONRPC: "2.0", ID: req.ID,
					Result: a2a.SendMessageResponse{Message: a2a.FromResponse(resp)},
				})
			})
		return
	}
	resp, attempts, err := s.router.Dispatch(r.Context(), ureq, opts...)
	if err != nil {
		s.logDispatchFailure(ureq, attempts, err)
		s.writeA2AError(w, req.ID, a2a.CodeInternal, upstreamErrorMessage(err))
		s.auditFailed("a2a", ureq.Model, comboName, start, err)
		return
	}
	s.auditDone("a2a", ureq.Model, comboName, start, resp.ProviderName, resp.Usage, false)
	writeJSON(w, http.StatusOK, a2a.Response{
		JSONRPC: "2.0", ID: req.ID,
		Result: a2a.SendMessageResponse{Message: a2a.FromResponse(resp)},
	})
}

// a2aStream 处理 SendStreamingMessage：任务生命周期流——首事件 Task
// (working) → 逐增量 artifactUpdate(append) → 终态 statusUpdate
// (completed/failed)。任务对象 transient：GetTask 不可查。
func (s *Server) a2aStream(w http.ResponseWriter, r *http.Request, req *a2a.Request, start time.Time) {
	ureq, opts, comboName, fusionReq, ctxID, ok := s.a2aPrepare(w, r, req)
	if !ok {
		return
	}
	if fusionReq {
		s.writeA2AError(w, req.ID, a2a.CodeUnsupportedOperation,
			"fusion does not support streaming (v1)")
		return
	}
	ureq.Stream = true // A2A 流式入口：上游必须以 SSE 回流（IR 由端点定性）
	stream, attempts, err := s.router.DispatchStream(r.Context(), ureq, opts...)
	if err != nil { // 首事件前失败：仍可回 JSON-RPC 错误（HTTP 200 信封）
		s.logDispatchFailure(ureq, attempts, err)
		s.writeA2AError(w, req.ID, a2a.CodeInternal, upstreamErrorMessage(err))
		s.auditFailed("a2a", ureq.Model, comboName, start, err)
		return
	}
	defer func() { _ = stream.Close() }()

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeA2AError(w, req.ID, a2a.CodeInternal, "streaming unsupported by transport")
		return
	}
	audit := s.beginStreamAudit("a2a", ureq.Model, comboName)
	taskID, random := "task-"+randomID(), randomID()
	if ctxID == "" {
		ctxID = "ctx-" + random
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(result a2a.StreamResponse) {
		b, err := json.Marshal(a2a.Response{JSONRPC: "2.0", ID: req.ID, Result: result})
		if err != nil {
			return
		}
		var sb strings.Builder
		sb.WriteString("data: ")
		sb.Write(b)
		sb.WriteString("\n\n")
		_, _ = io.WriteString(w, sb.String())
		flusher.Flush()
	}
	now := time.Now().UTC().Format(time.RFC3339)
	send(a2a.StreamResponse{Task: &a2a.Task{
		ID: taskID, ContextID: ctxID,
		Status: a2a.TaskStatus{State: a2a.StateWorking, Timestamp: now},
	}})

	winner := attemptWinner(attempts)
	var full strings.Builder
	for {
		chunk, err := stream.Next(r.Context())
		if err == io.EOF {
			break
		}
		if err != nil {
			if r.Context().Err() != nil {
				audit.finish(http.StatusOK, winner, "cancelled")
				return
			}
			s.log.Warn("a2a stream broken; closing with failed status", "err", err)
			send(a2a.StreamResponse{StatusUpdate: &a2a.TaskStatusUpdateEvent{
				TaskID: taskID, ContextID: ctxID,
				Status: a2a.TaskStatus{State: a2a.StateFailed, Timestamp: time.Now().UTC().Format(time.RFC3339)},
			}})
			audit.finish(http.StatusOK, winner, "stream_broken")
			return
		}
		audit.firstChunk()
		audit.observe(chunk)
		if text := a2a.ChunkText(chunk); text != "" {
			full.WriteString(text)
			send(a2a.StreamResponse{ArtifactUpdate: &a2a.TaskArtifactUpdateEvent{
				TaskID: taskID, ContextID: ctxID,
				Artifact: a2a.Artifact{ArtifactID: "text", Parts: []a2a.Part{a2a.TextPart(text)}},
				Append:   true,
			}})
		}
	}
	send(a2a.StreamResponse{StatusUpdate: &a2a.TaskStatusUpdateEvent{
		TaskID: taskID, ContextID: ctxID,
		Status: a2a.TaskStatus{
			State:     a2a.StateCompleted,
			Message:   &a2a.Message{MessageID: "msg-" + randomID(), Role: a2a.RoleAgent, Parts: []a2a.Part{a2a.TextPart(full.String())}},
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	}})
	audit.finish(http.StatusOK, winner, "")
}

// writeA2AError 以 JSON-RPC 信封写出错误（HTTP 200，错误在信封内）。
func (s *Server) writeA2AError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	writeJSON(w, http.StatusOK, a2a.Response{
		JSONRPC: "2.0", ID: id,
		Error: &a2a.RPCError{Code: code, Message: msg},
	})
}

// randomID 生成 16 hex 随机标识。
func randomID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}
