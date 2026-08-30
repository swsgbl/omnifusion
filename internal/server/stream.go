package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"sync"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/routing"
)

// sseFramePool 复用 SSE 事件帧缓冲（ item 2：转发 buffer
// 走 sync.Pool，热路径不逐请求 make）。
var sseFramePool = sync.Pool{
	New: func() any { return bytes.NewBuffer(make([]byte, 0, 1024)) },
}

// handleChatStream 处理 stream=true：buffer-first-chunk 已在路由层完成，
// 走到这里说明首事件已落地，直接开 200 并逐事件转发，不整体缓冲。
// 首事件之后的断流不再切换（"cannot un-ship bytes"），但客户端会收到
// 合成收尾帧 + [DONE] 的优雅收尾。
func (s *Server) handleChatStream(w http.ResponseWriter, r *http.Request,
	req *schema.UnifiedRequest, opts []routing.DispatchOption, audit *streamAudit) {
	stream, attempts, err := s.router.DispatchStream(r.Context(), req, opts...)
	if err != nil {
		s.logDispatchFailure(req, attempts, err)
		writeAPIError(w, http.StatusBadGateway, upstreamErrorMessage(err), "upstream_error", "")
		audit.finish(http.StatusBadGateway, "", dispatchErrKind(err))
		return
	}
	defer func() { _ = stream.Close() }()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError,
			"streaming unsupported by the underlying transport", "server_error", "")
		audit.finish(http.StatusInternalServerError, attemptWinner(attempts), "")
		return
	}

	setDegradedHeader(w, attemptDegraded(attempts))
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // 反向代理逐事件透传
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	s.forwardSSE(w, flusher, stream, r, audit, attemptWinner(attempts))
}

// streamTail 跟踪流式转发的收尾状态：流元数据与每个 choice 的
// 已见/已完成标记，供断流时合成收尾帧。
type streamTail struct {
	id, model string
	created   int64
	seen      map[int]bool
	finished  map[int]bool
}

func newStreamTail() *streamTail {
	return &streamTail{seen: map[int]bool{}, finished: map[int]bool{}}
}

// observe 记录一个已转发的 chunk 携带的元数据与 choice 状态。
func (t *streamTail) observe(c *schema.Chunk) {
	if c.ID != "" {
		t.id = c.ID
	}
	if c.Model != "" {
		t.model = c.Model
	}
	if c.Created != 0 {
		t.created = c.Created
	}
	for _, ch := range c.Choices {
		t.seen[ch.Index] = true
		if ch.FinishReason != "" {
			t.finished[ch.Index] = true
		}
	}
}

// unfinished 返回已出现但未收到 finish_reason 的 choice 序号（升序）；
// 一个 choice 都没见过时兜底 {0}——首 chunk 已落地，0 一定存在过。
func (t *streamTail) unfinished() []int {
	if len(t.seen) == 0 {
		return []int{0}
	}
	var out []int
	for idx := range t.seen {
		if !t.finished[idx] {
			out = append(out, idx)
		}
	}
	sort.Ints(out)
	return out
}

// forwardSSE 把归一化事件流逐帧写给客户端：每个 chunk 立即 Flush
// （TTFT 验收面，见 ）。上游正常收尾补 [DONE]；中途断流
// 则为未完成的 choice 合成 finish_reason=stop 收尾帧再补 [DONE]——
// 客户端拿到优雅收尾而非悬挂连接（与 anthropic end_turn /
// gemini STOP 的断流收尾口径一致）。
func (s *Server) forwardSSE(w io.Writer, flusher http.Flusher, stream provider.ChunkStream,
	r *http.Request, audit *streamAudit, winner string) {
	tail := newStreamTail()
	for {
		chunk, err := stream.Next(r.Context())
		if err == io.EOF {
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			flusher.Flush()
			audit.finish(http.StatusOK, winner, "")
			return
		}
		if err != nil {
			if r.Context().Err() != nil {
				audit.finish(http.StatusOK, winner, "cancelled") // 客户端已断开，无需收尾
				return
			}
			s.log.Warn("upstream stream broken after first chunk; closing with synthetic finish",
				"err", err)
			for _, idx := range tail.unfinished() {
				c := schema.NewChunk(tail.id, tail.model, tail.created)
				c.Choices = []schema.ChunkChoice{{Index: idx, FinishReason: schema.FinishStop}}
				writeSSEFrame(w, c)
			}
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			flusher.Flush()
			audit.finish(http.StatusOK, winner, "stream_broken")
			return
		}
		audit.firstChunk() // TTFT（幂等保最早）
		audit.observe(chunk)
		tail.observe(chunk)
		if !writeSSEFrame(w, chunk) {
			s.log.Warn("drop unserializable stream chunk", "chunk_id", chunk.ID)
			continue
		}
		flusher.Flush()
		if r.Context().Err() != nil {
			audit.finish(http.StatusOK, winner, "cancelled") // 客户端已断开
			return
		}
	}
}

// writeSSEFrame 从池中取缓冲拼一帧 "data: {json}\n\n"。
func writeSSEFrame(w io.Writer, chunk *schema.Chunk) bool {
	buf := sseFramePool.Get().(*bytes.Buffer)
	defer func() {
		buf.Reset()
		sseFramePool.Put(buf)
	}()
	buf.WriteString("data: ")
	if err := json.NewEncoder(buf).Encode(chunk); err != nil {
		return false
	}
	buf.WriteByte('\n') // Encode 已带一个 \n，再补一个构成空行定界
	_, err := w.Write(buf.Bytes())
	return err == nil
}
