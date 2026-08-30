// guardrails.go 是 server 侧 Guardrails 接线：三协议入站端点
// 在翻译后、路由分发前扫描正文文本——PII 命中按配置拦截（协议各自的
// 400 错误形状），注入模式命中默认告警放行（结构化日志，规则名+计数，
// 不落原文）。未装配（nil）即未启用，热路径零开销。
package server

import (
	"net/http"
	"strings"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// requestTexts 提取请求全部消息的文本部分（system/user/assistant 均扫）。
func requestTexts(req *schema.UnifiedRequest) []string {
	texts := make([]string, 0, len(req.Messages))
	for i := range req.Messages {
		if t := req.Messages[i].Content.TextOf(); t != "" {
			texts = append(texts, t)
		}
	}
	return texts
}

// endpointOf 把入站路径归一为审计端点名（chat/messages/gemini）。
func endpointOf(path string) string {
	switch {
	case strings.Contains(path, "chat"):
		return "chat"
	case strings.Contains(path, "messages"):
		return "messages"
	default:
		return "gemini"
	}
}

// guardAction 取该类发现的配置处置（指标标签用受控词表）。
func (s *Server) guardAction(kind string) string {
	if kind == "pii" {
		return string(s.guard.PIIAction())
	}
	return string(s.guard.InjectionAction())
}

// applyGuardrails 是三端点共用的检测挂点：返回 false 表示已拦截（协议
// 错误响应已由 errw 写出）；warn 命中记日志后放行。发现即计
// 指标（kind/rule/action 无原文），拦截请求落一行审计（provider=none）。
func (s *Server) applyGuardrails(path string, req *schema.UnifiedRequest,
	errw func(code int, msg string)) bool {
	if s.guard == nil {
		return true
	}
	rep := s.guard.Inspect(requestTexts(req))
	if len(rep.Findings) == 0 {
		return true
	}
	for _, f := range rep.Findings {
		s.metrics.RecordGuardrail(f.Kind, f.Rule, s.guardAction(f.Kind))
	}
	if !rep.Blocked {
		s.log.Warn("guardrails: injection pattern detected (allowed by policy)",
			"path", path, "findings", rep.Summary())
		return true
	}
	s.log.Warn("guardrails: request blocked",
		"path", path, "findings", rep.Summary())
	errw(http.StatusBadRequest,
		"guardrails: request blocked by policy ("+rep.Summary()+"); redact the sensitive data and retry")
	s.recordRequest(auditRecord{Endpoint: endpointOf(path), Model: req.Model,
		Status: http.StatusBadRequest, ErrKind: "guardrails", TTFTMS: -1})
	return false
}
