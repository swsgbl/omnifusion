// gemini_tools_test.go 钉住 M3.3 Gemini 工具面互译：tools/toolConfig
// 各模式映射、functionCall/functionResponse parts 双向（含 snake_case
// 解析）、上游聚合响应的 functionCall 解析、入站流编码器的整帧下发。
package translate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// TestGeminiToolConfigMap 覆盖 toolConfig 四模式双向映射。
func TestGeminiToolConfigMap(t *testing.T) {
	cases := []struct {
		wire *GeminiToolConfig
		ir   *schema.ToolChoice
	}{
		// AUTO 是 Gemini 默认：出站省略 toolConfig（见下方专项断言）。
		{&GeminiToolConfig{FunctionCallingConfig: &GeminiFunctionCallingConfig{Mode: "NONE"}},
			&schema.ToolChoice{Mode: schema.ToolChoiceNone}},
		{&GeminiToolConfig{FunctionCallingConfig: &GeminiFunctionCallingConfig{Mode: "ANY"}},
			&schema.ToolChoice{Mode: schema.ToolChoiceRequired}},
		{&GeminiToolConfig{FunctionCallingConfig: &GeminiFunctionCallingConfig{
			Mode: "ANY", AllowedFunctionNames: []string{"f"}}},
			&schema.ToolChoice{Mode: schema.ToolChoiceFunction, Function: "f"}},
	}
	for _, c := range cases {
		ir := geminiToolConfigFromWire(c.wire)
		if ir == nil || *ir != *c.ir {
			t.Errorf("fromWire(%+v) = %+v, want %+v", c.wire, ir, c.ir)
		}
		bw, _ := json.Marshal(geminiToolConfigToWire(c.ir))
		bi, _ := json.Marshal(c.wire)
		if string(bw) != string(bi) {
			t.Errorf("toWire(%+v) = %s, want %s", c.ir, bw, bi)
		}
	}
	if geminiToolConfigFromWire(nil) != nil || geminiToolConfigToWire(nil) != nil {
		t.Error("nil toolConfig must stay nil both ways")
	}
	if ir := geminiToolConfigFromWire(&GeminiToolConfig{FunctionCallingConfig: &GeminiFunctionCallingConfig{Mode: "AUTO"}}); ir == nil ||
		ir.Mode != schema.ToolChoiceAuto {
		t.Errorf("AUTO fromWire = %+v, want auto", ir)
	}
	// IR auto 是 Gemini 默认：出站省略 toolConfig。
	if geminiToolConfigToWire(&schema.ToolChoice{Mode: schema.ToolChoiceAuto}) != nil {
		t.Error("auto choice should render no toolConfig")
	}
}

// TestGeminiToolsRoundtrip 钉住 tools 数组 wire↔IR 等价（含 snake 解析）。
func TestGeminiToolsRoundtrip(t *testing.T) {
	wire := []GeminiTools{{FunctionDeclarations: []GeminiFunctionDecl{{
		Name:        "get_weather",
		Description: "Get weather",
		Parameters:  json.RawMessage(`{"type":"object"}`),
	}}}}
	ir := geminiToolsFromWire(wire)
	if len(ir) != 1 || ir[0].Function.Name != "get_weather" ||
		string(ir[0].Function.Parameters) != `{"type":"object"}` {
		t.Fatalf("fromWire = %+v", ir)
	}
	want, _ := json.Marshal(wire)
	got, _ := json.Marshal(geminiToolsToWire(ir))
	if string(got) != string(want) {
		t.Fatalf("roundtrip = %s, want %s", got, want)
	}
	var snake []GeminiTools
	if err := json.Unmarshal([]byte(
		`[{"function_declarations":[{"name":"f","parameters":{"type":"object"}}]}]`), &snake); err != nil ||
		len(snake) != 1 || len(snake[0].FunctionDeclarations) != 1 ||
		snake[0].FunctionDeclarations[0].Name != "f" {
		t.Fatalf("snake parse = %+v err=%v", snake, err)
	}
}

// TestGeminiToolMessagesRoundtrip：入站 wire→IR，再出站上游 wire→IR，
// 工具会话语义保持；id→name 映射由前文 assistant tool_calls 解出。
func TestGeminiToolMessagesRoundtrip(t *testing.T) {
	in := &GeminiRequest{
		SystemInstruction: &GeminiContent{Parts: []GeminiPart{{Text: "be brief"}}},
		Contents: []GeminiContent{
			{Role: "user", Parts: []GeminiPart{{Text: "Weather in Paris?"}}},
			{Role: "model", Parts: []GeminiPart{{FunctionCall: &GeminiFunctionCall{
				ID: "call_1", Name: "get_weather", Args: json.RawMessage(`{"city":"Paris"}`)}}}},
			{Role: "user", Parts: []GeminiPart{{FunctionResponse: &GeminiFunctionResponse{
				Name: "get_weather", Response: json.RawMessage(`{"temp":18}`)}}}},
			{Role: "user", Parts: []GeminiPart{{Text: "Thanks"}}},
		},
	}
	req, degraded := FromGeminiGenerateContent("gemini-x", in, false)
	if len(degraded) != 0 {
		t.Fatalf("degraded = %v, want none", degraded)
	}
	assertGeminiToolIR(t, "inbound", req)

	back, _ := FromGeminiGenerateContent("gemini-x", mustGeminiWire(t, req), false)
	assertGeminiToolIR(t, "roundtrip", back)
}

// assertGeminiToolIR 断言工具会话 IR 语义（含 system 首条）。
func assertGeminiToolIR(t *testing.T, label string, req *schema.UnifiedRequest) {
	t.Helper()
	if len(req.Messages) != 5 {
		t.Fatalf("%s: %d messages, want 5", label, len(req.Messages))
	}
	a := req.Messages[2]
	if a.Role != schema.RoleAssistant || len(a.ToolCalls) != 1 ||
		a.ToolCalls[0].ID != "call_1" ||
		a.ToolCalls[0].Function.Name != "get_weather" ||
		a.ToolCalls[0].Function.Arguments != `{"city":"Paris"}` {
		t.Fatalf("%s: assistant tool_calls = %+v", label, a.ToolCalls)
	}
	tool := req.Messages[3]
	if tool.Role != schema.RoleTool || tool.ToolCallID != "get_weather" ||
		tool.Content.TextOf() != `{"temp":18}` {
		t.Fatalf("%s: tool message = %+v", label, tool)
	}
}

// TestGeminiSnakePartParse 钉住 part 级 snake_case 兼容。
func TestGeminiSnakePartParse(t *testing.T) {
	var c GeminiContent
	raw := `{"parts":[{"function_call":{"name":"f","args":{"k":1}}},` +
		`{"function_response":{"name":"f","response":{"ok":true}}}]}`
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(c.Parts) != 2 || c.Parts[0].FunctionCall == nil ||
		c.Parts[0].FunctionCall.Name != "f" ||
		c.Parts[1].FunctionResponse == nil ||
		c.Parts[1].FunctionResponse.Name != "f" {
		t.Fatalf("parts = %+v", c.Parts)
	}
}

// TestFromGeminiUpstreamResponseFunctionCall：STOP + functionCall 强制
// finish=tool_calls；id 缺省回落 name。
func TestFromGeminiUpstreamResponseFunctionCall(t *testing.T) {
	resp := &GeminiResponse{
		ResponseID: "r9", ModelVersion: "gemini-x",
		Candidates: []GeminiCandidate{{
			Content: GeminiContent{Role: "model", Parts: []GeminiPart{
				{Text: "Checking."},
				{FunctionCall: &GeminiFunctionCall{
					Name: "get_weather", Args: json.RawMessage(`{"city":"Rome"}`)}},
			}},
			FinishReason: "STOP",
		}},
	}
	out := FromGeminiUpstreamResponse(resp)
	msg := out.Choices[0].Message
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "get_weather" ||
		msg.ToolCalls[0].Function.Arguments != `{"city":"Rome"}` {
		t.Fatalf("tool_calls = %+v", msg.ToolCalls)
	}
	if out.Choices[0].FinishReason != schema.FinishToolCalls {
		t.Fatalf("finish = %q, want tool_calls", out.Choices[0].FinishReason)
	}
}

// TestGeminiStreamEncoderToolCalls：arguments 碎片缓冲，finish 前整帧
// 下发完整 functionCall（每帧 parts 均为完整形）。
func TestGeminiStreamEncoderToolCalls(t *testing.T) {
	enc := NewGeminiStreamEncoder()
	zero := 0
	var out []string
	for _, frames := range [][][]byte{
		enc.Feed(&schema.Chunk{ID: "r1", Model: "gemini-x", Choices: []schema.ChunkChoice{{
			Delta: schema.Message{ToolCalls: []schema.ToolCall{{
				ID: "call_1", Type: "function", Index: &zero,
				Function: schema.ToolCallFunction{Name: "get_weather"},
			}}}}}}),
		enc.Feed(&schema.Chunk{Choices: []schema.ChunkChoice{{
			Delta: schema.Message{ToolCalls: []schema.ToolCall{{
				Index: &zero, Function: schema.ToolCallFunction{Arguments: `{"city":`}},
			}}}}}),
		enc.Feed(&schema.Chunk{Choices: []schema.ChunkChoice{{
			Delta: schema.Message{ToolCalls: []schema.ToolCall{{
				Index: &zero, Function: schema.ToolCallFunction{Arguments: `"Paris"}`}},
			}},
			FinishReason: schema.FinishToolCalls}}}),
	} {
		for _, f := range frames {
			out = append(out, string(f))
		}
	}
	for _, f := range enc.Finish() {
		out = append(out, string(f))
	}
	joined := strings.Join(out, "")
	// 碎片不得单独成帧（args 必须是完整合法 JSON）。
	for _, f := range out {
		var probe map[string]any
		if strings.Contains(f, "functionCall") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSuffix(
				strings.SplitN(f, "data: ", 2)[1], "\r\n\r\n"), "")), &probe); err != nil {
				t.Fatalf("frame with functionCall is not valid JSON: %v (%s)", err, f)
			}
		}
	}
	if !strings.Contains(joined, `"functionCall":{"id":"call_1","name":"get_weather","args":{"city":"Paris"}}`) {
		t.Errorf("stream missing complete functionCall:\n%s", joined)
	}
	if !strings.Contains(joined, `"finishReason":"STOP"`) {
		t.Errorf("stream missing finish frame:\n%s", joined)
	}
}
