// cachepolicy.go 是 Cache 2.0 的策略分类器（升级蓝图 Phase 4 /
// 施工方案 §五 CachePolicy）：任何缓存查询/回写前先过策略门。
//
// 默认策略（施工包硬性纪律 #9："不允许把工具调用、动态 prompt、随机
// 模型响应做成无条件 semantic cache"）：
//   - 请求带 tools / 上下文含 tool 角色消息      → BYPASS（agent 循环不可复用）
//   - 响应含 tool_calls / finish=tool_calls      → BYPASS（工具执行不可复用）
//   - 非确定性生成（temperature≠0 且未声明 seed）→ BYPASS（默认）；
//     seed 即显式 opt-in（同 seed 同输出的确定性声明）；
//     temperature=0 视为确定性采样，ALLOW
//
// 蓝图 P0-5：缓存投毒是现实攻击面——agent 循环里的缓存命中会把上一轮
// 的工具状态当成新答案返回。策略判定宁可错杀（bypass），缓存永不阻塞
// 主路径、永不把坏数据当命中（SemCache 既有纪律）。
package intelligence

import "github.com/swsgbl/omnifusion/internal/core/schema"

// CacheVerdict 是策略门的判定结果。
type CacheVerdict struct {
	Allowed bool
	// Reason 是机器可读的判定码（进审计/日志；ALLOW 时为 "ok"）。
	Reason string
}

// 判定码词表（进审计与排障日志）。
const (
	CacheOK               = "ok"
	CacheBypassTools      = "bypass_tool_schema"
	CacheBypassToolResult = "bypass_tool_result"
	CacheBypassToolCall   = "bypass_response_tool_call"
	CacheBypassNonDeterm  = "bypass_non_deterministic"
)

// EvaluateCachePolicy 对请求（及回写时的响应）做缓存资格判定。
// resp 传 nil 表示只判请求侧（查询路径）；回写路径传非 nil 以同时
// 检查响应侧（工具调用响应不可回写）。
func EvaluateCachePolicy(req *schema.UnifiedRequest, resp *schema.Response) CacheVerdict {
	// 1) 请求带工具声明：这是 agent 循环的标志——缓存命中会把上一轮
	//    的工具上下文当成新答案。
	if len(req.Tools) > 0 {
		return CacheVerdict{Allowed: false, Reason: CacheBypassTools}
	}
	// 2) 上下文含 tool 角色消息或 assistant 工具调用（循环中的中间态）。
	for i := range req.Messages {
		if req.Messages[i].Role == schema.RoleTool {
			return CacheVerdict{Allowed: false, Reason: CacheBypassToolResult}
		}
		if len(req.Messages[i].ToolCalls) > 0 {
			return CacheVerdict{Allowed: false, Reason: CacheBypassToolResult}
		}
	}
	// 3) 响应侧：含 tool_calls 或 finish=tool_calls 的响应不可复用。
	if resp != nil {
		for i := range resp.Choices {
			c := &resp.Choices[i]
			if len(c.Message.ToolCalls) > 0 || c.FinishReason == schema.FinishToolCalls {
				return CacheVerdict{Allowed: false, Reason: CacheBypassToolCall}
			}
		}
	}
	// 4) 非确定性生成：temperature≠0 且未声明 seed → BYPASS。
	//    seed 非 nil = 调用方的确定性 opt-in；temperature=0 = 确定性采样；
	//    temperature 未设 = 上游默认（通常 1.0）→ 按非确定性处理。
	if req.Seed == nil {
		if req.Temperature != nil && *req.Temperature != 0 {
			return CacheVerdict{Allowed: false, Reason: CacheBypassNonDeterm}
		}
		if req.Temperature == nil {
			return CacheVerdict{Allowed: false, Reason: CacheBypassNonDeterm}
		}
	}
	return CacheVerdict{Allowed: true, Reason: CacheOK}
}
