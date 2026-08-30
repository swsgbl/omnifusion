// combo.go 承载 组合路径的 server 侧编排：已知组合名判定 +
// per-path 压缩策略应用（路由组合绑定的压缩组合在此执行，
// §3 管线序 L4 压缩 → L3 路由）。
package server

import (
	"net/http"

	"github.com/swsgbl/omnifusion/internal/compression"
	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/routing"
)

// comboKnown 判定组合名是否已装配（键存在；值 nil = 纯路由组合）。
func (s *Server) comboKnown(name string) bool {
	_, ok := s.comboPipes[name]
	return ok
}

// comboCompress 应用组合绑定的压缩组合：改写 req.Messages 并返回
// WithPromptTokens（压缩后 token 喂给 L3 窗口过滤， 通道）。
// 无绑定/未装配是 no-op；管线内部的阶段失败与 gate 拦截已在
// Pipeline.Run 内兜底（原文直传），此处不再上抛。
func (s *Server) comboCompress(r *http.Request, req *schema.UnifiedRequest, combo string) []routing.DispatchOption {
	pipe := s.comboPipes[combo]
	if pipe == nil {
		return nil
	}
	sc := compression.NewStageContext(req.Model, r.Header.Get(routing.HeaderSession), req.Messages)
	before := compression.EstimateTokens(req.Messages)
	out, stats := pipe.Run(sc, req.Messages)
	after := compression.EstimateTokens(out)
	req.Messages = out
	s.cstats.record(combo, int64(before), int64(after), stats) // 压缩统计
	if s.log != nil {
		s.log.Info("combo compression applied",
			"combo", combo, "stages", len(stats),
			"before_tokens", before, "after_tokens", after)
	}
	return []routing.DispatchOption{routing.WithPromptTokens(int64(after))}
}
