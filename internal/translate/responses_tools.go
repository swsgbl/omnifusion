// responses_tools.go 承载 Responses 入站的工具/结构化输出映射与响应侧
// 渲染（与 responses.go 拆文件守 300 行规范）。工具定义从 Responses 的
// 扁平形展开为 IR 的 function 嵌套形；text.format 映射为 IR 的
// response_format（OpenAI Chat 形）；无 IR 对应的顶层字段记降级清单。
package translate

import (
	"encoding/json"
	"strings"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// responsesToolsFromWire 归一化工具、tool_choice、text.format 与杂项
// 降级字段（reasoning/metadata/parallel_tool_calls 无 IR 对应）。
func responsesToolsFromWire(in *ResponsesRequest) (
	[]schema.Tool, *schema.ToolChoice, json.RawMessage, []string) {
	var degraded []string
	var tools []schema.Tool
	for _, t := range in.Tools {
		if t.Type != "function" { // web_search/code_interpreter 等内置工具
			degraded = append(degraded, "tools."+t.Type)
			continue
		}
		tools = append(tools, schema.Tool{
			Type: "function",
			Function: schema.ToolFunction{
				Name: t.Name, Description: t.Description,
				Parameters: t.Parameters, Strict: t.Strict,
			},
		})
	}
	choice := responsesToolChoiceFromWire(in.ToolChoice)
	rf, rfDegraded := responsesFormatFromWire(in.Text)
	degraded = append(degraded, rfDegraded...)
	if len(in.Reasoning) > 0 && string(in.Reasoning) != "null" {
		degraded = append(degraded, "reasoning")
	}
	if len(in.Metadata) > 0 && string(in.Metadata) != "null" {
		degraded = append(degraded, "metadata")
	}
	if in.ParallelToolCall != nil {
		degraded = append(degraded, "parallel_tool_calls")
	}
	return tools, choice, rf, degraded
}

// responsesToolChoiceFromWire 收字符串（auto/none/required）或
// {"type":"function","name":...} 对象形。
func responsesToolChoiceFromWire(raw json.RawMessage) *schema.ToolChoice {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		var mode string
		if json.Unmarshal(raw, &mode) != nil {
			return nil
		}
		return &schema.ToolChoice{Mode: mode}
	}
	var obj struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &obj) != nil || obj.Type != "function" {
		return nil
	}
	return &schema.ToolChoice{Mode: schema.ToolChoiceFunction, Function: obj.Name}
}

// responsesFormatFromWire 把 text.format 映射为 IR response_format
// （OpenAI Chat 形）：json_object 直通；json_schema 重组为嵌套形。
func responsesFormatFromWire(t *ResponsesTextFormat) (json.RawMessage, []string) {
	if t == nil || t.Format == nil {
		return nil, nil
	}
	switch t.Format.Type {
	case "json_object":
		return json.RawMessage(`{"type":"json_object"}`), nil
	case "json_schema":
		obj := map[string]any{"type": "json_schema", "json_schema": map[string]any{
			"name": t.Format.Name, "schema": t.Format.Schema}}
		if t.Format.Strict != nil {
			obj["json_schema"].(map[string]any)["strict"] = *t.Format.Strict
		}
		b, err := json.Marshal(obj)
		if err != nil {
			return nil, []string{"text.format"}
		}
		return b, nil
	default: // text（自由文本）无需映射
		return nil, nil
	}
}

// ResponsesOutputItem 是响应 output 数组成员：message 或 function_call。
type ResponsesOutputItem struct {
	Type      string                `json:"type"`
	ID        string                `json:"id,omitempty"`
	Role      string                `json:"role,omitempty"`
	Status    string                `json:"status,omitempty"`
	Content   []ResponsesContentOut `json:"content,omitempty"`
	CallID    string                `json:"call_id,omitempty"`
	Name      string                `json:"name,omitempty"`
	Arguments string                `json:"arguments,omitempty"`
}

// ResponsesContentOut 是 message item 的输出片段（output_text）。
type ResponsesContentOut struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ResponsesAPIResponse 是非流式聚合响应体（object=response）。
type ResponsesAPIResponse struct {
	ID        string               `json:"id"`
	Object    string               `json:"object"`
	CreatedAt int64                `json:"created_at"`
	Status    string               `json:"status"`
	Model     string               `json:"model"`
	Output    []ResponsesOutputItem `json:"output"`
	Usage     *ResponsesUsageOut   `json:"usage,omitempty"`
}

// ResponsesUsageOut 是 Responses 口径的用量。
type ResponsesUsageOut struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// ToResponses 把中枢聚合响应渲染为 Responses API 形态：文本 parts 聚为
// 单条 assistant message item；工具调用逐条映射为 function_call item。
func ToResponses(resp *schema.Response) *ResponsesAPIResponse {
	out := &ResponsesAPIResponse{
		ID: responsesID(resp.ID), Object: "response",
		CreatedAt: resp.Created, Status: "completed", Model: resp.Model,
		Output: []ResponsesOutputItem{},
	}
	for _, ch := range resp.Choices {
		msg := ResponsesOutputItem{
			Type: "message", ID: responsesMsgID(resp.ID),
			Role: schema.RoleAssistant, Status: "completed",
		}
		for _, p := range ch.Message.Content.Parts {
			if p.Type == schema.PartText && p.Text != "" {
				msg.Content = append(msg.Content, ResponsesContentOut{Type: "output_text", Text: p.Text})
			}
		}
		if len(msg.Content) > 0 {
			out.Output = append(out.Output, msg)
		}
		for _, c := range ch.Message.ToolCalls {
			out.Output = append(out.Output, ResponsesOutputItem{
				Type: "function_call", ID: responsesCallID(c.ID),
				Status: "completed", CallID: c.ID, Name: c.Function.Name,
				Arguments: c.Function.Arguments,
			})
		}
	}
	if resp.Usage != nil {
		out.Usage = &ResponsesUsageOut{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
			TotalTokens:  resp.Usage.TotalTokens,
		}
	}
	return out
}

// responsesID 保证线上 id 形如 resp_…。
func responsesID(id string) string {
	if id == "" {
		return "resp_ofd"
	}
	if strings.HasPrefix(id, "resp_") {
		return id
	}
	return "resp_" + id
}

// responsesMsgID 派生 message item 的 id（msg_…）。
func responsesMsgID(id string) string {
	if id == "" {
		return "msg_ofd"
	}
	if strings.HasPrefix(id, "msg_") {
		return id
	}
	return "msg_" + id
}

// responsesCallID 派生 function_call item 的 id（fc_…）；call_id 保持
// 上游原值（客户端用它回配 function_call_output）。
func responsesCallID(callID string) string {
	if callID == "" {
		return "fc_ofd"
	}
	if strings.HasPrefix(callID, "fc_") {
		return callID
	}
	return "fc_" + callID
}
