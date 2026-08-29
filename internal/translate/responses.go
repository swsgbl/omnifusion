// responses.go 承载 OpenAI Responses API（POST /v1/responses）入站翻译对
// （遗留清单第 3 项，2026-08-29）：Codex CLI 与新一代 OpenAI SDK 默认走
// 该协议。请求侧 input（字符串或 item 数组）→ IR 消息；响应侧 IR →
// output items（message/function_call）。无 IR 对应的字段记入降级清单
// （调用方标记进 X-OmniFusion-Degraded），禁止静默丢弃（docs/04 §7）。
package translate

import (
	"encoding/json"
	"strings"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// ResponsesRequest 是 Responses API 请求体（Codex/OpenAI SDK 线上形态）。
type ResponsesRequest struct {
	Model            string               `json:"model"`
	Input            ResponsesInput       `json:"input"`
	Instructions     string               `json:"instructions,omitempty"`
	MaxOutputTokens  *int                 `json:"max_output_tokens,omitempty"`
	Temperature      *float64             `json:"temperature,omitempty"`
	TopP             *float64             `json:"top_p,omitempty"`
	Stream           bool                 `json:"stream,omitempty"`
	Tools            []ResponsesTool      `json:"tools,omitempty"`
	ToolChoice       json.RawMessage      `json:"tool_choice,omitempty"`
	Text             *ResponsesTextFormat `json:"text,omitempty"`
	Reasoning        json.RawMessage      `json:"reasoning,omitempty"`
	Metadata         json.RawMessage      `json:"metadata,omitempty"`
	ParallelToolCall *bool                `json:"parallel_tool_calls,omitempty"`
}

// ResponsesInput 接受字符串或 item 数组两种线上形态。
type ResponsesInput struct {
	Text  string
	Items []ResponsesItem
}

// UnmarshalJSON 收字符串（单条 user 消息）或 item 数组。
func (i *ResponsesInput) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if strings.HasPrefix(s, `"`) {
		return json.Unmarshal(data, &i.Text)
	}
	if s == "" || s == "null" {
		return nil
	}
	return json.Unmarshal(data, &i.Items)
}

// MarshalJSON 保持线上形态（文本输入为主，测试/回显用）。
func (i ResponsesInput) MarshalJSON() ([]byte, error) {
	if len(i.Items) == 0 {
		return json.Marshal(i.Text)
	}
	return json.Marshal(i.Items)
}

// ResponsesItem 是 input 数组的一个成员：message / function_call /
// function_call_output（未知类型经 degraded 标注，内容不丢——拼为文本）。
type ResponsesItem struct {
	Type      string                     `json:"type,omitempty"`
	Role      string                     `json:"role,omitempty"`
	Content   ResponsesItemContent       `json:"content,omitempty"`
	Name      string                     `json:"name,omitempty"`
	Arguments string                     `json:"arguments,omitempty"`
	CallID    string                     `json:"call_id,omitempty"`
	Output    string                     `json:"output,omitempty"`
	Raw       map[string]json.RawMessage `json:"-"`
}

// ResponsesItemContent 接受字符串或 content part 数组。
type ResponsesItemContent struct {
	Text  string
	Parts []ResponsesContentPart
}

// UnmarshalJSON 收字符串或 parts 数组。
func (c *ResponsesItemContent) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if strings.HasPrefix(s, `"`) {
		return json.Unmarshal(data, &c.Text)
	}
	if s == "" || s == "null" {
		return nil
	}
	return json.Unmarshal(data, &c.Parts)
}

// ResponsesContentPart 是 message item 的内容片段（input_text/
// output_text/input_image…，未知类型保留原文文本或 Raw）。
type ResponsesContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL string          `json:"image_url,omitempty"`
	Raw      json.RawMessage `json:"-"`
}

// ResponsesTool 是 Responses 形工具定义：扁平字段（区别于 Chat
// Completions 的 function 嵌套）。仅 function 类型有 IR 对应。
type ResponsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// ResponsesTextFormat 是 text.format（结构化输出声明）。
type ResponsesTextFormat struct {
	Format *ResponsesFormatSpec `json:"format,omitempty"`
}

// ResponsesFormatSpec 是 text.format.format 的具体形态。
type ResponsesFormatSpec struct {
	Type   string          `json:"type"` // text | json_object | json_schema
	Name   string          `json:"name,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
	Strict *bool           `json:"strict,omitempty"`
}

// FromResponses 把入站请求归一化为中枢 IR。
func FromResponses(in *ResponsesRequest) (*schema.UnifiedRequest, []string) {
	req := &schema.UnifiedRequest{
		Model:       in.Model,
		Stream:      in.Stream,
		Temperature: in.Temperature,
		TopP:        in.TopP,
		MaxTokens:   in.MaxOutputTokens,
	}
	if in.Instructions != "" {
		req.Messages = append(req.Messages, schema.Message{
			Role: schema.RoleSystem, Content: schema.NewTextContent(in.Instructions),
		})
	}
	if in.Input.Text != "" {
		req.Messages = append(req.Messages, schema.Message{
			Role: schema.RoleUser, Content: schema.NewTextContent(in.Input.Text),
		})
	}
	for _, it := range in.Input.Items {
		req.Messages = append(req.Messages, responsesItemToMessages(it)...)
	}
	tools, choice, rf, degraded := responsesToolsFromWire(in)
	req.Tools = tools
	req.ToolChoice = choice
	req.ResponseFormat = rf
	return req, degraded
}

// responsesItemToMessages 把单个 input item 展开为 IR 消息（0..n 条）。
func responsesItemToMessages(it ResponsesItem) []schema.Message {
	switch {
	case it.Type == "function_call" || (it.Type == "" && it.CallID != "" && it.Name != ""):
		return []schema.Message{{
			Role: schema.RoleAssistant,
			ToolCalls: []schema.ToolCall{{
				ID: it.CallID, Type: "function",
				Function: schema.ToolCallFunction{Name: it.Name, Arguments: it.Arguments},
			}},
		}}
	case it.Type == "function_call_output":
		return []schema.Message{{
			Role: schema.RoleTool, ToolCallID: it.CallID,
			Content: schema.NewTextContent(it.Output),
		}}
	case it.Type == "message" || (it.Type == "" && it.Role != ""):
		return []schema.Message{{Role: responsesRole(it.Role), Content: responsesContentToIR(it.Content)}}
	default: // 未知 item：内容并入 user 文本，不静默丢
		return []schema.Message{{Role: schema.RoleUser,
			Content: schema.NewTextContent(responsesItemFallbackText(it))}}
	}
}

// responsesRole 归一角色（developer → system，Responses 特有）。
func responsesRole(role string) string {
	switch role {
	case schema.RoleUser, schema.RoleAssistant, schema.RoleSystem, schema.RoleTool:
		return role
	case "developer", "":
		return schema.RoleSystem
	default:
		return role
	}
}

// responsesItemFallbackText 未知 item 的保守文本化。
func responsesItemFallbackText(it ResponsesItem) string {
	if it.Output != "" {
		return it.Output
	}
	if it.Text() != "" {
		return it.Text()
	}
	b, _ := json.Marshal(it)
	return string(b)
}

// Text 返回 item content 的纯文本（简写）。
func (it ResponsesItem) Text() string {
	if it.Content.Text != "" {
		return it.Content.Text
	}
	var sb strings.Builder
	for _, p := range it.Content.Parts {
		sb.WriteString(p.Text)
	}
	return sb.String()
}

// responsesContentToIR 把 content parts 映射为 IR Content。
func responsesContentToIR(c ResponsesItemContent) schema.Content {
	if c.Text != "" {
		return schema.NewTextContent(c.Text)
	}
	out := schema.Content{Parts: make([]schema.Part, 0, len(c.Parts))}
	for _, p := range c.Parts {
		switch p.Type {
		case "input_text", "output_text", "summary_text":
			out.Parts = append(out.Parts, schema.Part{Type: schema.PartText, Text: p.Text})
		case "input_image":
			out.Parts = append(out.Parts, schema.Part{Type: schema.PartImageURL,
				ImageURL: &schema.ImageURL{URL: p.ImageURL}})
		default: // input_file 等未建模：保留原文
			out.Parts = append(out.Parts, schema.Part{Type: schema.PartText, Text: p.Text})
		}
	}
	return out
}
