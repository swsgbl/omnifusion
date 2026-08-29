// matrix_tools_test.go 钉住工具会话的矩阵一致性（M3.3）：三种入站
// wire 对同一 canonical 工具语义（tools+tool_choice+assistant 调用+
// tool 结果）归一，canonical IR 向三种上游出站再回读语义保持。
package translate

import (
	"encoding/json"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

const (
	canonToolName = "get_weather"
	canonToolArgs = `{"city":"Paris"}`
	canonToolResp = `{"temp":18}`
	canonCallID   = "call_1"
	canonToolDecl = `{"type":"object","properties":{"city":{"type":"string"}}}`
)

// canonicalToolIR 是工具会话的中枢语义基准。
func canonicalToolIR() *schema.UnifiedRequest {
	return &schema.UnifiedRequest{
		Model: canonModel,
		Messages: []schema.Message{
			{Role: schema.RoleSystem, Content: schema.NewTextContent(canonSystem)},
			{Role: "user", Content: schema.NewTextContent("Weather in Paris?")},
			{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{
				ID: canonCallID, Type: "function",
				Function: schema.ToolCallFunction{Name: canonToolName, Arguments: canonToolArgs},
			}}},
			{Role: schema.RoleTool, ToolCallID: canonCallID,
				Content: schema.NewTextContent(canonToolResp)},
			{Role: "user", Content: schema.NewTextContent("Thanks")},
		},
		Tools: []schema.Tool{{
			Type: "function",
			Function: schema.ToolFunction{
				Name: canonToolName, Description: "Get weather",
				Parameters: json.RawMessage(canonToolDecl),
			},
		}},
		ToolChoice: &schema.ToolChoice{Mode: schema.ToolChoiceRequired},
	}
}

// toolInboundOpenAI 是 OpenAI 形工具会话 wire fixture。
var toolInboundOpenAI = `{"model":"` + canonModel + `",` +
	`"messages":[{"role":"system","content":"` + canonSystem + `"},` +
	`{"role":"user","content":"Weather in Paris?"},` +
	`{"role":"assistant","tool_calls":[{"id":"` + canonCallID + `","type":"function",` +
	`"function":{"name":"` + canonToolName + `","arguments":` +
	jsonString(canonToolArgs) + `}}]},` +
	`{"role":"tool","tool_call_id":"` + canonCallID + `","content":` +
	jsonString(canonToolResp) + `},` +
	`{"role":"user","content":"Thanks"}],` +
	`"tools":[{"type":"function","function":{"name":"` + canonToolName + `",` +
	`"description":"Get weather","parameters":` + canonToolDecl + `}}],` +
	`"tool_choice":"required"}`

// toolInboundAnthropic 是 Anthropic 形工具会话 wire fixture。
func toolInboundAnthropic() *AnthropicRequest {
	return &AnthropicRequest{
		Model: canonModel, MaxTokens: canonMaxTok,
		System: schema.NewTextContent(canonSystem),
		Messages: []AnthropicMessage{
			{Role: "user", Content: schema.NewTextContent("Weather in Paris?")},
			{Role: "assistant", Content: schema.Content{Parts: []schema.Part{
				{Type: "tool_use", Raw: json.RawMessage(
					`{"type":"tool_use","id":"` + canonCallID + `","name":"` + canonToolName +
						`","input":{"city":"Paris"}}`)}}}},
			{Role: "user", Content: schema.Content{Parts: []schema.Part{
				{Type: "tool_result", Raw: json.RawMessage(
					`{"type":"tool_result","tool_use_id":"` + canonCallID +
						`","content":"{\"temp\":18}"}`)}}}},
			{Role: "user", Content: schema.NewTextContent("Thanks")},
		},
		Tools:      []AnthropicTool{{Name: canonToolName, Description: "Get weather", InputSchema: json.RawMessage(canonToolDecl)}},
		ToolChoice: &AnthropicToolChoice{Type: "any"},
	}
}

// toolInboundGemini 是 Gemini 形工具会话 wire fixture。
func toolInboundGemini() *GeminiRequest {
	return &GeminiRequest{
		SystemInstruction: &GeminiContent{Parts: []GeminiPart{{Text: canonSystem}}},
		Contents: []GeminiContent{
			{Role: "user", Parts: []GeminiPart{{Text: "Weather in Paris?"}}},
			{Role: "model", Parts: []GeminiPart{{FunctionCall: &GeminiFunctionCall{
				ID: canonCallID, Name: canonToolName, Args: json.RawMessage(`{"city":"Paris"}`)}}}},
			{Role: "user", Parts: []GeminiPart{{FunctionResponse: &GeminiFunctionResponse{
				Name: canonToolName, Response: json.RawMessage(`{"temp":18}`)}}}},
			{Role: "user", Parts: []GeminiPart{{Text: "Thanks"}}},
		},
		Tools:      []GeminiTools{{FunctionDeclarations: []GeminiFunctionDecl{{Name: canonToolName, Description: "Get weather", Parameters: json.RawMessage(canonToolDecl)}}}},
		ToolConfig: &GeminiToolConfig{FunctionCallingConfig: &GeminiFunctionCallingConfig{Mode: "ANY"}},
	}
}

// jsonString 把文本编码为 JSON 字符串字面量（含引号）。
func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

// assertToolReq 断言 IR 与 canonical 工具会话语义等价。
func assertToolReq(t *testing.T, label string, req *schema.UnifiedRequest) {
	t.Helper()
	want := canonicalToolIR()
	if len(req.Messages) != len(want.Messages) {
		t.Fatalf("%s: %d messages, want %d", label, len(req.Messages), len(want.Messages))
	}
	for i, m := range req.Messages {
		w := want.Messages[i]
		if m.Role != w.Role {
			t.Errorf("%s: msg[%d].role = %q, want %q", label, i, m.Role, w.Role)
		}
		if len(w.ToolCalls) != len(m.ToolCalls) {
			t.Fatalf("%s: msg[%d] tool_calls = %+v", label, i, m.ToolCalls)
		}
		for j, c := range m.ToolCalls {
			wc := w.ToolCalls[j]
			if c.ID != wc.ID || c.Function.Name != wc.Function.Name ||
				c.Function.Arguments != wc.Function.Arguments {
				t.Errorf("%s: msg[%d].call[%d] = %+v", label, i, j, c)
			}
		}
		// Gemini 的 functionResponse 只带 name 不带调用 id：经 Gemini
		// 跳板后 tool 消息身份退化为函数名（协议不对称，允许两态）。
		if m.ToolCallID != w.ToolCallID && m.ToolCallID != canonToolName {
			t.Errorf("%s: msg[%d].tool_call_id = %q, want %q or %q", label, i,
				m.ToolCallID, w.ToolCallID, canonToolName)
		}
		if m.Content.TextOf() != w.Content.TextOf() {
			t.Errorf("%s: msg[%d].text = %q, want %q", label, i,
				m.Content.TextOf(), w.Content.TextOf())
		}
	}
	if len(req.Tools) != 1 || req.Tools[0].Function.Name != canonToolName ||
		string(req.Tools[0].Function.Parameters) != canonToolDecl {
		t.Errorf("%s: tools = %+v", label, req.Tools)
	}
	if req.ToolChoice == nil || req.ToolChoice.Mode != schema.ToolChoiceRequired {
		t.Errorf("%s: tool_choice = %+v, want required", label, req.ToolChoice)
	}
}

// TestMatrixToolRequests 覆盖工具会话 3 入站 × 3 上游 = 9 格。
func TestMatrixToolRequests(t *testing.T) {
	var openaiIR schema.UnifiedRequest
	if err := json.Unmarshal([]byte(toolInboundOpenAI), &openaiIR); err != nil {
		t.Fatalf("openai fixture: %v", err)
	}
	anthropicIR, degraded := FromAnthropicMessages(toolInboundAnthropic())
	if len(degraded) != 0 {
		t.Errorf("anthropic fixture degraded = %v", degraded)
	}
	geminiIR, degraded := FromGeminiGenerateContent(canonModel, toolInboundGemini(), false)
	if len(degraded) != 0 {
		t.Errorf("gemini fixture degraded = %v", degraded)
	}
	for label, ir := range map[string]*schema.UnifiedRequest{
		"openai→IR":    &openaiIR,
		"anthropic→IR": anthropicIR,
		"gemini→IR":    geminiIR,
	} {
		assertToolReq(t, label, ir)
		for upLabel, roundtrip := range upstreamRoundtrips(ir) {
			assertToolReq(t, label+"→"+upLabel+"→IR", roundtrip)
		}
	}
}

// TestMatrixToolResponses 覆盖工具响应 3 出站渲染 × 3 本协议回读：
// tool_calls 语义与 finish=tool_calls 在三种 wire 形上保持。
func TestMatrixToolResponses(t *testing.T) {
	canonical := &schema.Response{
		ID: "c1", Object: "chat.completion", Model: canonModel,
		Choices: []schema.ResponseChoice{{
			Message: schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{
				ID: canonCallID, Type: "function",
				Function: schema.ToolCallFunction{Name: canonToolName, Arguments: canonToolArgs},
			}}},
			FinishReason: schema.FinishToolCalls,
		}},
		Usage: &schema.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	}
	rendered := map[string][]byte{}
	if b, err := json.Marshal(canonical); err == nil {
		rendered["openai"] = b
	}
	if b, err := json.Marshal(ToAnthropicMessages(canonical)); err == nil {
		rendered["anthropic"] = b
	}
	if b, err := json.Marshal(ToGeminiGenerateContent(canonical)); err == nil {
		rendered["gemini"] = b
	}
	readBack := map[string]func([]byte) (*schema.Response, error){
		"openai": func(b []byte) (*schema.Response, error) {
			var o schema.Response
			err := json.Unmarshal(b, &o)
			return &o, err
		},
		"anthropic": func(b []byte) (*schema.Response, error) {
			var ar AnthropicResponse
			if err := json.Unmarshal(b, &ar); err != nil {
				return nil, err
			}
			return FromAnthropicUpstreamResponse(&ar), nil
		},
		"gemini": func(b []byte) (*schema.Response, error) {
			var gr GeminiResponse
			if err := json.Unmarshal(b, &gr); err != nil {
				return nil, err
			}
			return FromGeminiUpstreamResponse(&gr), nil
		},
	}
	for proto, wire := range rendered {
		resp, err := readBack[proto](wire)
		if err != nil {
			t.Fatalf("%s: read back: %v", proto, err)
		}
		label := proto + "→IR"
		calls := resp.Choices[0].Message.ToolCalls
		if len(calls) != 1 || calls[0].ID != canonCallID ||
			calls[0].Function.Name != canonToolName ||
			calls[0].Function.Arguments != canonToolArgs {
			t.Errorf("%s: tool_calls = %+v", label, calls)
		}
		if resp.Choices[0].FinishReason != schema.FinishToolCalls {
			t.Errorf("%s: finish = %q, want tool_calls", label, resp.Choices[0].FinishReason)
		}
	}
}
