package translate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// decodeResponses 解析请求体（含 input 双态解码）。
func decodeResponses(t *testing.T, body string) *ResponsesRequest {
	t.Helper()
	var in ResponsesRequest
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &in
}

func TestFromResponsesStringInput(t *testing.T) {
	in := decodeResponses(t, `{"model":"gpt-x","input":"hello","instructions":"be brief",
		"max_output_tokens":128,"temperature":0.3}`)
	req, degraded := FromResponses(in)
	if req.Model != "gpt-x" || req.Stream {
		t.Fatalf("model/stream = %s/%v", req.Model, req.Stream)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != schema.RoleSystem ||
		req.Messages[0].Content.TextOf() != "be brief" ||
		req.Messages[1].Role != schema.RoleUser || req.Messages[1].Content.TextOf() != "hello" {
		t.Fatalf("messages = %+v", req.Messages)
	}
	if req.MaxTokens == nil || *req.MaxTokens != 128 {
		t.Fatalf("max_tokens = %+v", req.MaxTokens)
	}
	if req.Temperature == nil || *req.Temperature != 0.3 {
		t.Fatalf("temperature = %+v", req.Temperature)
	}
	if len(degraded) != 0 {
		t.Fatalf("degraded = %v, want empty", degraded)
	}
}

func TestFromResponsesItemsWithToolRoundTrip(t *testing.T) {
	in := decodeResponses(t, `{"model":"m","input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"run it"}]},
		{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"ls\"}","call_id":"call_1"},
		{"type":"function_call_output","call_id":"call_1","output":"file-a\nfile-b"}
	]}`)
	req, _ := FromResponses(in)
	if len(req.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(req.Messages))
	}
	if req.Messages[0].Role != schema.RoleUser || req.Messages[0].Content.TextOf() != "run it" {
		t.Fatalf("user msg = %+v", req.Messages[0])
	}
	fc := req.Messages[1]
	if fc.Role != schema.RoleAssistant || len(fc.ToolCalls) != 1 ||
		fc.ToolCalls[0].ID != "call_1" || fc.ToolCalls[0].Function.Name != "shell" ||
		fc.ToolCalls[0].Function.Arguments != `{"cmd":"ls"}` {
		t.Fatalf("function_call msg = %+v", fc)
	}
	fo := req.Messages[2]
	if fo.Role != schema.RoleTool || fo.ToolCallID != "call_1" || fo.Content.TextOf() != "file-a\nfile-b" {
		t.Fatalf("function_call_output msg = %+v", fo)
	}
}

func TestFromResponsesToolsAndChoice(t *testing.T) {
	in := decodeResponses(t, `{"model":"m","input":"hi",
		"tools":[{"type":"function","name":"f1","description":"d","parameters":{"type":"object"}},
			{"type":"web_search_preview"}],
		"tool_choice":{"type":"function","name":"f1"},
		"text":{"format":{"type":"json_schema","name":"out","schema":{"type":"object"},"strict":true}},
		"reasoning":{"effort":"high"},"metadata":{},"parallel_tool_calls":true}`)
	req, degraded := FromResponses(in)
	if len(req.Tools) != 1 || req.Tools[0].Function.Name != "f1" ||
		req.Tools[0].Function.Description != "d" ||
		string(req.Tools[0].Function.Parameters) != `{"type":"object"}` {
		t.Fatalf("tools = %+v", req.Tools)
	}
	if req.ToolChoice == nil || req.ToolChoice.Mode != schema.ToolChoiceFunction ||
		req.ToolChoice.Function != "f1" {
		t.Fatalf("tool_choice = %+v", req.ToolChoice)
	}
	var rf map[string]any
	if err := json.Unmarshal(req.ResponseFormat, &rf); err != nil {
		t.Fatalf("response_format invalid: %v", err)
	}
	if rf["type"] != "json_schema" {
		t.Fatalf("response_format = %s", req.ResponseFormat)
	}
	want := map[string]bool{"tools.web_search_preview": false, "reasoning": false,
		"metadata": false, "parallel_tool_calls": false}
	for _, d := range degraded {
		if _, ok := want[d]; !ok {
			t.Fatalf("unexpected degraded %q", d)
		}
		want[d] = true
	}
	for k, seen := range want {
		if !seen {
			t.Fatalf("degraded missing %q (got %v)", k, degraded)
		}
	}
}

func TestToResponsesTextAndTools(t *testing.T) {
	resp := schema.NewResponse("c9", "model-a", 42)
	resp.Choices = []schema.ResponseChoice{{
		Index: 0,
		Message: schema.Message{
			Role:    schema.RoleAssistant,
			Content: schema.NewTextContent("answer"),
			ToolCalls: []schema.ToolCall{{
				ID: "call_7", Type: "function",
				Function: schema.ToolCallFunction{Name: "shell", Arguments: `{"cmd":"go test"}`},
			}},
		},
		FinishReason: schema.FinishToolCalls,
	}}
	resp.Usage = &schema.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}

	out := ToResponses(resp)
	if !strings.HasPrefix(out.ID, "resp_") || out.Object != "response" ||
		out.Status != "completed" || out.CreatedAt != 42 {
		t.Fatalf("head = %+v", out)
	}
	if len(out.Output) != 2 {
		t.Fatalf("output items = %d, want 2 (message + function_call)", len(out.Output))
	}
	msg := out.Output[0]
	if msg.Type != "message" || msg.Role != "assistant" ||
		len(msg.Content) != 1 || msg.Content[0].Type != "output_text" || msg.Content[0].Text != "answer" {
		t.Fatalf("message item = %+v", msg)
	}
	fc := out.Output[1]
	if fc.Type != "function_call" || fc.CallID != "call_7" || fc.Name != "shell" ||
		fc.Arguments != `{"cmd":"go test"}` {
		t.Fatalf("function_call item = %+v", fc)
	}
	if out.Usage == nil || out.Usage.InputTokens != 11 || out.Usage.OutputTokens != 7 ||
		out.Usage.TotalTokens != 18 {
		t.Fatalf("usage = %+v", out.Usage)
	}
}

func TestFromResponsesStringToolChoiceAndJSON_object(t *testing.T) {
	in := decodeResponses(t, `{"model":"m","input":"x","tool_choice":"auto",
		"text":{"format":{"type":"json_object"}}}`)
	req, _ := FromResponses(in)
	if req.ToolChoice == nil || req.ToolChoice.Mode != "auto" {
		t.Fatalf("tool_choice = %+v", req.ToolChoice)
	}
	if string(req.ResponseFormat) != `{"type":"json_object"}` {
		t.Fatalf("response_format = %s", req.ResponseFormat)
	}
}

func TestFromResponsesLegacyItemsNoType(t *testing.T) { // 无 type 但有 role 的旧形态
	in := decodeResponses(t, `{"model":"m","input":[{"role":"user","content":"q"}]}`)
	req, _ := FromResponses(in)
	if len(req.Messages) != 1 || req.Messages[0].Role != schema.RoleUser ||
		req.Messages[0].Content.TextOf() != "q" {
		t.Fatalf("messages = %+v", req.Messages)
	}
}
