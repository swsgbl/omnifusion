// fusion.go 是 M6.1 "@fusion" 指令的 HTTP 边界（三端点共用）：
// 未装配 400、流式 400（v1 合成是批操作）、缓存查询/回写复用
// SemCache 语义（模型命名空间 "@fusion"），扇出经 router.WithTarget
// 定向分发（隔离/配额/打分照常生效）。协议形态由端点注入
// （writeErr/writeResp），本文件不做协议翻译。
package server

import (
	"context"
	"net/http"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/routing"
)

// handleFusion 执行 @fusion 请求：缓存命中直返；否则扇出 → 门控 →
// 合成。model 是审计面使用的请求模型（"@fusion"）；comboName 供审计
// 与默认压缩（已在此前应用）。writeErr/writeResp 由端点注入协议形态。
func (s *Server) handleFusion(w http.ResponseWriter, r *http.Request, req *schema.UnifiedRequest,
	endpoint, model, comboName string, start time.Time,
	writeErr func(status int, msg string), writeResp func(resp *schema.Response)) {
	if s.fusion == nil {
		writeErr(http.StatusBadRequest,
			"fusion is not configured; set fusion.members in the gateway config")
		return
	}
	if req.Stream {
		writeErr(http.StatusBadRequest,
			"fusion does not support streaming (v1); resend with stream=false")
		return
	}
	// L5 语义缓存（M4.6 语义同三端点）：@fusion 是独立模型命名空间。
	if resp, ok := s.cache.Lookup(r.Context(), req); ok {
		w.Header().Set("X-OmniFusion-Cache", "hit")
		writeResp(resp)
		s.auditDone(endpoint, model, comboName, start, "cache", resp.Usage, true)
		return
	}
	res, err := s.fusion.Execute(r.Context(), req, s.fusionDispatch)
	if err != nil {
		writeErr(http.StatusBadGateway, "fusion failed: "+err.Error())
		s.auditFailed(endpoint, model, comboName, start, err)
		return
	}
	if res.Synthesized {
		w.Header().Set("X-OmniFusion-Fusion", "synthesized")
	} else {
		w.Header().Set("X-OmniFusion-Fusion", "single") // 门控未过/Judge 失败的降级直通
	}
	if res.JudgeErr != nil && s.log != nil {
		s.log.Warn("fusion degraded to single member", "judge_err", res.JudgeErr)
	}
	resp := res.Response
	w.Header().Set("X-OmniFusion-Cache", "miss")
	writeResp(resp)
	s.auditDone(endpoint, model, comboName, start, resp.ProviderName, resp.Usage, false)
	// 合成/直通终稿是确定值：异步回写缓存（WithoutCancel 防断连中断）。
	go s.cache.WriteBack(context.WithoutCancel(r.Context()), req, resp)
}

// fusionDispatch 是 FusionRunner 的定向分发原语：WithTarget 跳过候选
// 选择，成员失败原样上抛（门控/降级由 Fusion 层接管）。
func (s *Server) fusionDispatch(ctx context.Context, provider, model string, req *schema.UnifiedRequest) (*schema.Response, error) {
	resp, _, err := s.router.Dispatch(ctx, req, routing.WithTarget(provider, model))
	return resp, err
}
