// anthropic_tools_test.go 钉住 Anthropic 工具面互译：tools/
// tool_choice 各模式映射、消息内 tool_use/tool_result blocks 双向、
// 上游聚合响应的 tool_use 解析、入站流编码器的工具块事件序列。
package translate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// TestAnthropicToolChoiceMap 覆盖 tool_choice 四模式双向映射。
func TestAnthropicToolChoiceMap(t *testing.T) {
	cases := []struct {
		wire AnthropicToolChoice
		ir   schema.ToolChoice
	}{
		{AnthropicToolChoice{Type: "auto"}, schema.ToolChoice{Mode: schema.ToolChoiceAuto}},
		{AnthropicToolChoice{Type: "none"}, schema.ToolChoice{Mode: schema.ToolChoiceNone}},
		{AnthropicToolChoice{Type: "any"}, schema.ToolChoice{Mode: schema.ToolChoiceRequired}},
		{AnthropicToolChoice{Type: "tool", Name: "f"}, schema.ToolChoice{
			Mode: schema.ToolChoiceFunction, Function: "f"}},
	}
	for _, c := range cases {
		ir := anthropicToolChoiceFromWire(&c.wire)
		if ir == nil || *ir != c.ir {
			t.Errorf("fromWire(%+v) = %+v, want %+v", c.wire, ir, c.ir)
		}
		wire := anthropicToolChoiceToWire(&c.ir)
		if wire == nil || *wire != c.wire {
			t.Errorf("toWire(%+v) = %+v, want %+v", c.ir, wire, c.wire)
		}
	}
	if anthropicToolChoiceFromWire(nil) != nil || anthropicToolChoiceToWire(nil) != nil {
		t.Error("nil tool_choice must stay nil both ways")
	}
}

// TestAnthropicToolsRoundtrip 钉住 tools 数组 wire↔IR 等价。
func TestAnthropicToolsRoundtrip(t *testing.T) {
	wire := []AnthropicTool{{
		Name:        "get_weather",
		Description: "Get weather",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}}
	ir := anthropicToolsFromWire(wire)
	if len(ir) != 1 || ir[0].Type != "function" ||
		ir[0].Function.Name != "get_weather" ||
		string(ir[0].Function.Parameters) != `{"type":"object"}` {
		t.Fatalf("fromWire = %+v", ir)
	}
	back := anthropicToolsToWire(ir)
	want, _ := json.Marshal(wire)
	got, _ := json.Marshal(back)
	if string(got) != string(want) {
		t.Fatalf("roundtrip = %s, want %s", got, want)
	}
}

// anthropicToolFixture 构造带工具会话的入站 wire（tool_use + tool_result）。
func anthropicToolFixture() *AnthropicRequest {
	return &AnthropicRequest{
		Model:     "claude-x",
		MaxTokens: 64,
		System:    schema.NewTextContent("be brief"),
		Messages: []AnthropicMessage{
			{Role: "user", Content: schema.Content{Parts: []schema.Part{
				{Type: schema.PartText, Text: "Weather in Paris?"}}}},
			{Role: "assistant", Content: schema.Content{Parts: []schema.Part{
				{Type: "tool_use", Raw: json.RawMessage(
					`{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Paris"}}`)}}}},
			{Role: "user", Content: schema.Content{Parts: []schema.Part{
				{Type: "tool_result", Raw: json.RawMessage(
					`{"type":"tool_result","tool_use_id":"toolu_1","content":"{\"temp\":18}"}`)}}}},
			{Role: "user", Content: schema.NewTextContent("Thanks")},
		},
	}
}

// TestAnthropicToolMessagesRoundtrip：入站 wire→IR，再出站上游 wire→IR，
// 工具会话语义保持（跨协议组合经 IR 中枢的直通格自洽性）。
func TestAnthropicToolMessagesRoundtrip(t *testing.T) {
	req, degraded := FromAnthropicMessages(anthropicToolFixture())
	if len(degraded) != 0 {
		t.Fatalf("degraded = %v, want none", degraded)
	}
	assertAnthropicToolIR(t, "inbound", req)

	up, _ := ToAnthropicUpstreamRequest(req)
	back, _ := FromAnthropicMessages(up)
	assertAnthropicToolIR(t, "roundtrip", back)
}

// assertAnthropicToolIR 断言工具会话 IR 语义（含 system 首条）。
func assertAnthropicToolIR(t *testing.T, label string, req *schema.UnifiedRequest) {
	t.Helper()
	if len(req.Messages) != 5 {
		t.Fatalf("%s: %d messages, want 5", label, len(req.Messages))
	}
	if req.Messages[0].Role != schema.RoleSystem {
		t.Fatalf("%s: msg[0].role = %q, want system", label, req.Messages[0].Role)
	}
	a := req.Messages[2]
	if a.Role != schema.RoleAssistant || len(a.ToolCalls) != 1 ||
		a.ToolCalls[0].ID != "toolu_1" ||
		a.ToolCalls[0].Function.Name != "get_weather" ||
		a.ToolCalls[0].Function.Arguments != `{"city":"Paris"}` {
		t.Fatalf("%s: assistant tool_calls = %+v", label, a.ToolCalls)
	}
	tool := req.Messages[3]
	if tool.Role != schema.RoleTool || tool.ToolCallID != "toolu_1" ||
		tool.Content.TextOf() != `{"temp":18}` {
		t.Fatalf("%s: tool message = %+v", label, tool)
	}
}

// TestFromAnthropicUpstreamResponseToolUse 覆盖上游聚合响应的 tool_use。
func TestFromAnthropicUpstreamResponseToolUse(t *testing.T) {
	resp := &AnthropicResponse{
		ID: "msg_9", Model: "claude-x", StopReason: "tool_use",
		Content: []AnthropicBlock{
			{Type: "text", Text: "Checking."},
			{Type: "tool_use", ID: "toolu_2", Name: "get_weather",
				Input: json.RawMessage(`{"city":"Rome"}`)},
		},
	}
	out := FromAnthropicUpstreamResponse(resp)
	msg := out.Choices[0].Message
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "toolu_2" ||
		msg.ToolCalls[0].Function.Arguments != `{"city":"Rome"}` {
		t.Fatalf("tool_calls = %+v", msg.ToolCalls)
	}
	if out.Choices[0].FinishReason != schema.FinishToolCalls {
		t.Fatalf("finish = %q, want tool_calls", out.Choices[0].FinishReason)
	}
}

// TestAnthropicStreamEncoderToolCalls 覆盖入站流编码：delta.tool_calls
// → content_block_start(tool_use)+input_json_delta+stop，块 index 递增。
func TestAnthropicStreamEncoderToolCalls(t *testing.T) {
	enc := NewAnthropicStreamEncoder()
	zero := 0
	var sb strings.Builder
	batches := [][][]byte{
		enc.Feed(&schema.Chunk{ID: "c1", Model: "m", Choices: []schema.ChunkChoice{{
			Delta: schema.Message{ToolCalls: []schema.ToolCall{{
				ID: "toolu_1", Type: "function", Index: &zero,
				Function: schema.ToolCallFunction{Name: "get_weather"},
			}}}}}}),
		enc.Feed(&schema.Chunk{Choices: []schema.ChunkChoice{{
			Delta: schema.Message{ToolCalls: []schema.ToolCall{{
				Index:    &zero,
				Function: schema.ToolCallFunction{Arguments: `{"city":"Paris"}`},
			}}}}}}),
		enc.Feed(&schema.Chunk{Choices: []schema.ChunkChoice{{
			FinishReason: schema.FinishToolCalls}}}),
		enc.Finish(),
	}
	for _, frames := range batches {
		for _, f := range frames {
			sb.Write(f)
		}
	}
	out := sb.String()
	want := []string{"message_start", "content_block_start", "ping",
		"content_block_start", "content_block_delta",
		"content_block_stop", "content_block_stop", "message_delta", "message_stop"}
	if got := eventNames(out); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for _, s := range []string{
		`"index":1`, `"type":"tool_use"`, `"id":"toolu_1"`, `"name":"get_weather"`,
		`"partial_json":"{\"city\":\"Paris\"}"`, `"stop_reason":"tool_use"`,
	} {
		if !strings.Contains(out, s) {
			t.Errorf("stream missing %s", s)
		}
	}
}
