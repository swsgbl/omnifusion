// memory.go 承载 会话记忆的 HTTP 挂点。逐请求 opt-in：请求头
// X-OmniFusion-Memory: on 同时开启记录与召回，默认关闭 = 零行为变更、
// 零落盘（隐私红线，无 config 开关——恒装配、惰性生效）。召回注入
// 在 guardrails 之后（注入内容源自已过护栏的本库数据，且不得绕过对
// 入站正文的护栏）、dispatchOptions 之前；记录在非流式分发成功后旁
// 路执行（流式 v1 不记录：完成点无聚合响应）。
package server

import (
	"net/http"
	"strconv"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/intelligence"
	"github.com/swsgbl/omnifusion/internal/routing"
)

// HeaderMemory 是会话记忆 opt-in 请求头；值 "on" 开启。
const HeaderMemory = "X-OmniFusion-Memory"

// memoryRecallLimit 每次召回注入的命中条数上限。
const memoryRecallLimit = 4

// memoryEnabled 判定请求是否开启会话记忆（逐请求 opt-in）。
func (s *Server) memoryEnabled(r *http.Request) bool {
	return r.Header.Get(HeaderMemory) == "on"
}

// memoryRecall 召回注入：以末条 user 消息为查询，命中时把
// InjectMemoryMessage 前插到 messages 头部并给出 X-OmniFusion-Memory-Hits
// 响应头。永不阻断、永不改写失败路径（检索失败在 Recall 内吞掉）。
func (s *Server) memoryRecall(w http.ResponseWriter, r *http.Request, req *schema.UnifiedRequest) {
	if !s.memoryEnabled(r) || s.memory == nil {
		return
	}
	hits := s.memory.Recall(intelligence.LastUserText(req), memoryRecallLimit)
	if len(hits) == 0 {
		return
	}
	msg := intelligence.InjectMemoryMessage(hits)
	if msg == nil {
		return
	}
	req.Messages = append([]schema.Message{*msg}, req.Messages...)
	w.Header().Set("X-OmniFusion-Memory-Hits", strconv.Itoa(len(hits)))
	s.log.Info("memory recall", "hits", len(hits), "session", r.Header.Get(routing.HeaderSession))
}

// memoryRecord 记录回合：由各端点非流式成功路径以 go 旁路调用
//（Memory.Record 本身不收 ctx、旁路写入不受客户端断开影响——第三
// 轮 CI 审计后不再收 ctx 参数，unparam 确认其从未被使用）。头缺席、
// 未装配记忆、无 X-Session-Id 均为 no-op。
func (s *Server) memoryRecord(r *http.Request, req *schema.UnifiedRequest, resp *schema.Response) {
	if !s.memoryEnabled(r) || s.memory == nil {
		return
	}
	s.memory.Record(r.Header.Get(routing.HeaderSession), req, resp)
}
