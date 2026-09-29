package intelligence

import (
	"encoding/json"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// Cache 2.0 策略门契约测试（蓝图 Phase 4 验收：缓存投毒回归测试）。

func temp(v float64) *float64 { return &v }
func seed(v int64) *int64     { return &v }

func reqWith(opts func(*schema.UnifiedRequest)) *schema.UnifiedRequest {
	r := &schema.UnifiedRequest{Model: "m", Messages: []schema.Message{
		{Role: schema.RoleUser, Content: schema.NewTextContent("hi")},
	}}
	if opts != nil {
		opts(r)
	}
	return r
}

func respText() *schema.Response {
	return &schema.Response{Choices: []schema.ResponseChoice{{
		Message: schema.Message{Role: schema.RoleAssistant,
			Content: schema.NewTextContent("ok")},
		FinishReason: "stop",
	}}}
}

func respToolCall() *schema.Response {
	return &schema.Response{Choices: []schema.ResponseChoice{{
		Message: schema.Message{Role: schema.RoleAssistant,
			ToolCalls: []schema.ToolCall{{ID: "c1", Function: schema.ToolCallFunction{Name: "f"}}}},
		FinishReason: schema.FinishToolCalls,
	}}}
}

func TestPolicyToolsInRequestBypass(t *testing.T) {
	v := EvaluateCachePolicy(reqWith(func(r *schema.UnifiedRequest) {
		r.Tools = []schema.Tool{{Function: schema.ToolFunction{Name: "f"}}}
	}), nil)
	if v.Allowed {
		t.Errorf("tools in request must bypass: %+v", v)
	}
	if v.Reason != CacheBypassTools {
		t.Errorf("reason = %s", v.Reason)
	}
}

func TestPolicyToolResultInContextBypass(t *testing.T) {
	v := EvaluateCachePolicy(reqWith(func(r *schema.UnifiedRequest) {
		r.Messages = append(r.Messages,
			schema.Message{Role: schema.RoleTool, Content: schema.NewTextContent("result")})
	}), nil)
	if v.Allowed || v.Reason != CacheBypassToolResult {
		t.Errorf("tool result in context must bypass: %+v", v)
	}
	// assistant 发起过工具调用的上下文同样 bypass
	v = EvaluateCachePolicy(reqWith(func(r *schema.UnifiedRequest) {
		r.Messages = append(r.Messages, schema.Message{Role: schema.RoleAssistant,
			ToolCalls: []schema.ToolCall{{ID: "c", Function: schema.ToolCallFunction{Name: "f"}}}})
	}), nil)
	if v.Allowed || v.Reason != CacheBypassToolResult {
		t.Errorf("assistant tool_calls in context must bypass: %+v", v)
	}
}

func TestPolicyResponseToolCallBypass(t *testing.T) {
	req := reqWith(func(r *schema.UnifiedRequest) { r.Temperature = temp(0) })
	v := EvaluateCachePolicy(req, respToolCall())
	if v.Allowed || v.Reason != CacheBypassToolCall {
		t.Errorf("tool-call response must bypass writeback: %+v", v)
	}
	// finish_reason 亦触发
	resp := respText()
	resp.Choices[0].FinishReason = schema.FinishToolCalls
	if v := EvaluateCachePolicy(req, resp); v.Allowed {
		t.Errorf("finish=tool_calls must bypass: %+v", v)
	}
}

func TestPolicyNonDeterministicBypass(t *testing.T) {
	// temperature=0.7 无 seed → BYPASS
	v := EvaluateCachePolicy(reqWith(func(r *schema.UnifiedRequest) {
		r.Temperature = temp(0.7)
	}), nil)
	if v.Allowed || v.Reason != CacheBypassNonDeterm {
		t.Errorf("temp>0 no seed must bypass: %+v", v)
	}
	// temperature 未设（上游默认）→ BYPASS
	if v := EvaluateCachePolicy(reqWith(nil), nil); v.Allowed {
		t.Error("nil temperature must bypass (provider default is non-deterministic)")
	}
	// seed 声明 → opt-in ALLOW
	v = EvaluateCachePolicy(reqWith(func(r *schema.UnifiedRequest) {
		r.Temperature = temp(0.7)
		r.Seed = seed(42)
	}), nil)
	if !v.Allowed {
		t.Errorf("seed opt-in must allow: %+v", v)
	}
}

func TestPolicyDeterministicAllow(t *testing.T) {
	// temperature=0 → 确定性采样 ALLOW
	v := EvaluateCachePolicy(reqWith(func(r *schema.UnifiedRequest) {
		r.Temperature = temp(0)
	}), respText())
	if !v.Allowed || v.Reason != CacheOK {
		t.Errorf("temp=0 must allow: %+v", v)
	}
	// seed + tools? 不行——tools 是硬 bypass，seed 不能翻案
	v = EvaluateCachePolicy(reqWith(func(r *schema.UnifiedRequest) {
		r.Temperature = temp(0)
		r.Seed = seed(1)
		r.Tools = []schema.Tool{{Function: schema.ToolFunction{Name: "f"}}}
	}), nil)
	if v.Allowed {
		t.Error("tools hard-bypass cannot be overridden by seed")
	}
}

func TestPolicyAllowRoundTrip(t *testing.T) {
	req := reqWith(func(r *schema.UnifiedRequest) { r.Temperature = temp(0) })
	if v := EvaluateCachePolicy(req, respText()); !v.Allowed {
		t.Fatalf("precondition failed: %+v", v)
	}
	// JSON 序列化不改变判定（缓存键走序列化路径）
	b, _ := json.Marshal(req)
	var back schema.UnifiedRequest
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if v := EvaluateCachePolicy(&back, respText()); !v.Allowed {
		t.Errorf("round-trip changed verdict: %+v", v)
	}
}
