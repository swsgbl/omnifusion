// a2a.go 是 A2A v1.0 协议的 HTTP 边界：AgentCard 发现端点
// （公开）+ JSON-RPC 2.0 /rpc（网关 key 鉴权）。SendMessage 走
// Message-only；SendStreamingMessage 走任务生命周期流。装配
// TaskStore（SetA2ATasks）后任务持久化——GetTask/CancelTask/
// ListTasks 可查可取消（蓝图 Phase 7 任务面，复用 Phase 6 TaskStore）；
// 未装配保持 transient 兼容语义（任务只在流生命周期内存在）。
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/swsgbl/omnifusion/internal/a2a"
	"github.com/swsgbl/omnifusion/internal/agent"
	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/obs"
	"github.com/swsgbl/omnifusion/internal/routing"
)

// SetA2A 注入 A2A AgentCard 与缺省目标模型。未装配（nil card）
// 时不注册 /.well-known/agent-card.json 与 /rpc 路由。
func (s *Server) SetA2A(card *a2a.AgentCard, defaultModel string) {
	s.a2aCard = card
	s.a2aModel = defaultModel
}

// SetA2ATasks 注入任务存储：装配后流式任务持久化（重启存活），
// GetTask/CancelTask/ListTasks/SubscribeToTask 生效。nil = transient
// 兼容（旧语义）。
func (s *Server) SetA2ATasks(ts *agent.TaskStore) {
	s.a2aTasks = ts
	s.a2aAct = map[string]context.CancelFunc{}
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
	case "GetTask":
		s.a2aGetTask(w, &req)
	case "CancelTask":
		s.a2aCancelTask(w, &req)
	case "ListTasks":
		s.a2aListTasks(w, &req)
	case "SubscribeToTask":
		s.a2aSubscribe(w, r, &req)
	default:
		s.writeA2AError(w, req.ID, a2a.CodeMethodNotFound, "unknown method "+req.Method)
	}
}

// a2aTaskParams 是 GetTask/CancelTask/SubscribeToTask 的公共参数。
type a2aTaskParams struct {
	ID string `json:"id"`
}

// a2aGetTask 返回持久任务快照（状态 + 完成态产物）。
func (s *Server) a2aGetTask(w http.ResponseWriter, req *a2a.Request) {
	if s.a2aTasks == nil {
		s.writeA2AError(w, req.ID, a2a.CodeTaskNotFound,
			"gateway agent is stateless; tasks are transient (stream-only)")
		return
	}
	var p a2aTaskParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil || p.ID == "" {
			s.writeA2AError(w, req.ID, a2a.CodeInvalidParams, "params.id is required")
			return
		}
	}
	t, ok := s.a2aTasks.Get(p.ID)
	if !ok {
		s.writeA2AError(w, req.ID, a2a.CodeTaskNotFound, "task "+p.ID+" not found")
		return
	}
	writeJSON(w, http.StatusOK, a2a.Response{JSONRPC: "2.0", ID: req.ID, Result: a2aTaskOf(t)})
}

// a2aCancelTask 取消任务：先杀活流（注册表里的取消函数），再落
// 持久终态。终态不可取消（canceled 幂等重放回显）。
func (s *Server) a2aCancelTask(w http.ResponseWriter, req *a2a.Request) {
	if s.a2aTasks == nil {
		s.writeA2AError(w, req.ID, a2a.CodeTaskNotFound,
			"gateway agent is stateless; tasks are transient (stream-only)")
		return
	}
	var p a2aTaskParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil || p.ID == "" {
			s.writeA2AError(w, req.ID, a2a.CodeInvalidParams, "params.id is required")
			return
		}
	}
	if cancel := s.a2aActiveCancel(p.ID); cancel != nil {
		cancel() // 活流自杀；流循环会 Cancel 落库（幂等重放安全）
	}
	t, err := s.a2aTasks.Cancel(p.ID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			s.writeA2AError(w, req.ID, a2a.CodeTaskNotFound, "task "+p.ID+" not found")
		} else {
			s.writeA2AError(w, req.ID, a2a.CodeNotCancelable, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, a2a.Response{JSONRPC: "2.0", ID: req.ID, Result: a2aTaskOf(t)})
}

// a2aListTasks 列出本网关的 A2A 任务（UpdatedAt 新者在前，上限 50）。
func (s *Server) a2aListTasks(w http.ResponseWriter, req *a2a.Request) {
	if s.a2aTasks == nil {
		s.writeA2AError(w, req.ID, a2a.CodeUnsupportedOperation,
			"task listing is not supported (stateless gateway agent)")
		return
	}
	tasks := s.a2aTasks.List("a2a", 50)
	out := make([]a2a.Task, 0, len(tasks))
	for i := range tasks {
		out = append(out, a2aTaskOf(&tasks[i]))
	}
	writeJSON(w, http.StatusOK, a2a.Response{JSONRPC: "2.0", ID: req.ID,
		Result: map[string]any{"tasks": out}})
}

// a2aSubscribe 以 SSE 回放任务当前状态（快照语义：一次事件即关闭。
// 活任务的后续迁移不跟随——重连客户端对终态任务可拿到确定结论，
// 对活跃任务拿到 working 快照后轮询 GetTask）。
func (s *Server) a2aSubscribe(w http.ResponseWriter, _ *http.Request, req *a2a.Request) {
	if s.a2aTasks == nil {
		s.writeA2AError(w, req.ID, a2a.CodeTaskNotFound,
			"gateway agent is stateless; tasks are transient (stream-only)")
		return
	}
	var p a2aTaskParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil || p.ID == "" {
			s.writeA2AError(w, req.ID, a2a.CodeInvalidParams, "params.id is required")
			return
		}
	}
	t, ok := s.a2aTasks.Get(p.ID)
	if !ok {
		s.writeA2AError(w, req.ID, a2a.CodeTaskNotFound, "task "+p.ID+" not found")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeA2AError(w, req.ID, a2a.CodeInternal, "streaming unsupported by transport")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	ev := a2a.TaskStatusUpdateEvent{
		TaskID:    t.ID,
		ContextID: t.ContextID,
		Status:    a2aTaskOf(t).Status,
	}
	b, err := json.Marshal(a2a.Response{JSONRPC: "2.0", ID: req.ID, Result: ev})
	if err != nil {
		return
	}
	_, _ = io.WriteString(w, "data: "+string(b)+"\n\n")
	flusher.Flush()
}

// a2aTaskOf 把存储任务投影为 A2A 线上任务对象（状态映射 + 完成
// 产物 + 失败原因消息）。
func a2aTaskOf(t *agent.Task) a2a.Task {
	state := a2a.StateWorking
	var msg *a2a.Message
	switch t.Status {
	case agent.TaskStatusCompleted:
		state = a2a.StateCompleted
	case agent.TaskStatusCanceled:
		state = a2a.StateCanceled
	case agent.TaskStatusFailed, agent.TaskStatusTimedOut:
		state = a2a.StateFailed
		msg = &a2a.Message{
			MessageID: "err-" + t.ID, Role: a2a.RoleAgent,
			Parts: []a2a.Part{a2a.TextPart(t.Err)},
		}
	case agent.TaskStatusCreated, agent.TaskStatusRunning:
		state = a2a.StateWorking
	}
	task := a2a.Task{
		ID: t.ID, ContextID: t.ContextID,
		Status: a2a.TaskStatus{State: state, Message: msg, Timestamp: t.UpdatedAt.UTC().Format(time.RFC3339)},
	}
	if t.Status == agent.TaskStatusCompleted && t.Result != "" {
		task.Artifacts = []a2a.Artifact{{
			ArtifactID: "text", Name: "response",
			Parts: []a2a.Part{a2a.TextPart(t.Result)},
		}}
	}
	return task
}

// a2aRegisterActive 登记活流取消函数；返回注销器（defer 调用）。
func (s *Server) a2aRegisterActive(taskID string, cancel context.CancelFunc) func() {
	s.a2aActMu.Lock()
	defer s.a2aActMu.Unlock()
	if s.a2aAct == nil {
		s.a2aAct = map[string]context.CancelFunc{}
	}
	s.a2aAct[taskID] = cancel
	return func() {
		s.a2aActMu.Lock()
		defer s.a2aActMu.Unlock()
		delete(s.a2aAct, taskID)
	}
}

// a2aActiveCancel 取活流取消函数（无则 nil）。
func (s *Server) a2aActiveCancel(taskID string) context.CancelFunc {
	s.a2aActMu.Lock()
	defer s.a2aActMu.Unlock()
	return s.a2aAct[taskID]
}

// a2aPrepare 完成 SendMessage/流式共用的前置：参数解码 → IR 翻译 →
// 护栏 → 路由选项（策略/组合/会话亲和/钉选）→ 组合压缩绑定。
// fusion 请求返回 fusionReq=true（调用方分流到 handleFusion）。
func (s *Server) a2aPrepare(w http.ResponseWriter, r *http.Request, req *a2a.Request) (
	ureq *schema.UnifiedRequest, opts []routing.DispatchOption, comboName string, fusionReq bool, ctxID string, routeSrc routing.ReasonCode, ok bool) {
	var params a2a.SendMessageParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			s.writeA2AError(w, req.ID, a2a.CodeInvalidParams, "params: "+err.Error())
			return nil, nil, "", false, "", "", false
		}
	}
	ureq, err := a2a.ToUnified(&params.Message, s.a2aModel)
	if err != nil {
		code := a2a.CodeInvalidParams
		if err == a2a.ErrNoContent {
			code = a2a.CodeContentTypeNotSupport
		}
		s.writeA2AError(w, req.ID, code, err.Error())
		return nil, nil, "", false, "", "", false
	}
	if ureq.Model == "" {
		s.writeA2AError(w, req.ID, a2a.CodeInvalidParams,
			"no target model: set message.metadata.model or a2a.default_model in the gateway config")
		return nil, nil, "", false, "", "", false
	}
	if !s.applyGuardrails("/rpc", ureq, func(code int, msg string) {
		s.writeA2AError(w, req.ID, a2a.CodeInvalidParams, "guardrails: "+msg)
	}) {
		return nil, nil, "", false, "", "", false
	}
	opts, comboName, fusionReq, routeSrc, err = s.dispatchOptions(r, ureq)
	if err != nil {
		s.writeA2AError(w, req.ID, a2a.CodeInvalidParams, err.Error())
		return nil, nil, "", false, "", "", false
	}
	if fusionReq {
		return ureq, nil, comboName, true, params.Message.ContextID, routeSrc, true
	}
	if params.Message.ContextID != "" { // A2A contextId → 会话亲和（sticky）
		opts = append(opts, routing.WithSession(params.Message.ContextID))
	}
	opts = append(opts, s.pinOption()...)
	if comboName != "" {
		opts = append(opts, s.comboCompress(r, ureq, comboName)...)
	}
	return ureq, opts, comboName, false, params.Message.ContextID, routeSrc, true
}

// a2aSend 处理非流式 SendMessage：Message-only 响应（简单交互不建任务）。
func (s *Server) a2aSend(w http.ResponseWriter, r *http.Request, req *a2a.Request, start time.Time) {
	ureq, opts, comboName, fusionReq, ctxID, routeSrc, ok := s.a2aPrepare(w, r, req)
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
	dec := routing.FoldDecision(attempts, err, ureq.Model, r.Header.Get("X-Request-Id"),
		routeSrc, time.Since(start).Milliseconds(), r.Context().Err() != nil)
	s.logRouteDecision("a2a", dec)
	s.logGenAI(&obs.GenAICorrelation{
		Operation:      obs.OpA2ASend,
		System:         dec.ChosenProvider,
		RequestModel:   ureq.Model,
		ResponseModel:  dec.ResolvedModel,
		RequestID:      r.Header.Get("X-Request-Id"),
		ConversationID: ctxID,
	}, time.Since(start), err == nil)
	if err != nil {
		s.logDispatchFailure(ureq, attempts, err)
		s.writeA2AError(w, req.ID, a2a.CodeInternal, upstreamErrorMessage(err))
		s.auditFailed("a2a", ureq.Model, comboName, start, err)
		return
	}
	w.Header().Set("X-OmniFusion-Route", dec.Summary())
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
	ureq, opts, comboName, fusionReq, ctxID, _, ok := s.a2aPrepare(w, r, req)
	if !ok {
		return
	}
	if fusionReq {
		s.writeA2AError(w, req.ID, a2a.CodeUnsupportedOperation,
			"fusion does not support streaming (v1)")
		return
	}
	ureq.Stream = true // A2A 流式入口：上游必须以 SSE 回流（IR 由端点定性）
	// 持久任务（蓝图 Phase 7）：TaskStore 落生命周期，流事件用同一
	// taskID——GetTask/CancelTask/ListTasks 事后可查可取消（重启存活）。
	// 未装配（nil）退回 transient 语义（随机 ID，仅流内存在）。
	taskID := "task-" + randomID()
	if s.a2aTasks != nil {
		if t, _, err := s.a2aTasks.Create("a2a", "", a2aStreamTaskTTL); err == nil {
			taskID = t.ID
			_, _ = s.a2aTasks.Start(taskID)
			if ctxID != "" {
				_ = s.a2aTasks.AttachContext(taskID, ctxID)
			}
		}
	}
	if ctxID == "" {
		ctxID = "ctx-" + randomID()
	}
	// 活流注册：CancelTask 可中途打断。streamCtx 是上游请求的父级
	//（DispatchStream 由此派生）——取消它同时打断上游读取，否则
	// 取消只对本地循环生效、上游挂死（E2E 实测教训）。
	streamCtx, cancelStream := context.WithCancel(r.Context())
	defer cancelStream()
	if s.a2aTasks != nil {
		defer s.a2aRegisterActive(taskID, cancelStream)()
	}
	stream, attempts, err := s.router.DispatchStream(streamCtx, ureq, opts...)
	if err != nil { // 首事件前失败：仍可回 JSON-RPC 错误（HTTP 200 信封）
		s.logDispatchFailure(ureq, attempts, err)
		s.writeA2AError(w, req.ID, a2a.CodeInternal, upstreamErrorMessage(err))
		s.auditFailed("a2a", ureq.Model, comboName, start, err)
		if s.a2aTasks != nil {
			_, _ = s.a2aTasks.Fail(taskID, upstreamErrorMessage(err))
		}
		return
	}
	defer func() { _ = stream.Close() }()

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeA2AError(w, req.ID, a2a.CodeInternal, "streaming unsupported by transport")
		return
	}
	audit := s.beginStreamAudit("a2a", ureq.Model, comboName)
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
	streamDone := false // 终态一次性关联行（Phase 9）
	defer func() {
		s.logGenAI(&obs.GenAICorrelation{
			Operation: obs.OpA2AStream, System: winner,
			RequestModel: ureq.Model, RequestID: string(req.ID),
			ConversationID: ctxID, TaskID: taskID,
		}, time.Since(start), streamDone)
	}()
	var full strings.Builder
	for {
		chunk, err := stream.Next(streamCtx)
		if err == io.EOF {
			break
		}
		if err != nil {
			if streamCtx.Err() != nil {
				if s.a2aTasks != nil {
					_, _ = s.a2aTasks.Cancel(taskID) // 客户端断开/CancelTask：落终态
				}
				audit.finish(http.StatusOK, winner, "cancelled")
				return
			}
			s.log.Warn("a2a stream broken; closing with failed status", "err", err)
			if s.a2aTasks != nil {
				_, _ = s.a2aTasks.Fail(taskID, err.Error())
			}
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
	if s.a2aTasks != nil {
		_, _ = s.a2aTasks.Complete(taskID, full.String()) // 全文=最终证据
	}
	streamDone = true
	audit.finish(http.StatusOK, winner, "")
}

// a2aStreamTaskTTL 是持久流任务的默认截止（防御性：流挂死时任务
// 不会永久 running——超时后读时判 timed_out）。
const a2aStreamTaskTTL = 30 * time.Minute

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
