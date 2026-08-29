// anthropic_tools.go 承载 Anthropic 工具面互译（M3.3）：tools 数组、
// tool_choice 与消息内的 tool_use / tool_result blocks 双向映射。
// 出站侧 blocks 经 Part.Raw 原样序列化（复用 schema.Part 的透传机制）。
package translate

import (
	"encoding/json"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// AnthropicTool 是 tools 数组元素（input_schema 即 JSON Schema）。
type AnthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// AnthropicToolChoice 是 tool_choice 字段：auto/any/tool/none。
type AnthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// toolUseBlock 是 assistant 消息里的工具调用 block。
type toolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// toolResultBlock 是 user 消息里的工具结果 block（content 取文本形）。
type toolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
}

// anthropicToolsFromWire 解析入站 tools 数组为 IR（空数组归 nil）。
func anthropicToolsFromWire(wire []AnthropicTool) []schema.Tool {
	if len(wire) == 0 {
		return nil
	}
	out := make([]schema.Tool, 0, len(wire))
	for _, t := range wire {
		out = append(out, schema.Tool{
			Type: "function",
			Function: schema.ToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}
	return out
}

// anthropicToolsToWire 渲染 IR tools 为 wire 形。
func anthropicToolsToWire(tools []schema.Tool) []AnthropicTool {
	out := make([]AnthropicTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, AnthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: t.Function.Parameters,
		})
	}
	return out
}

// anthropicToolChoiceFromWire 解析入站 tool_choice；未知形态返回 nil。
func anthropicToolChoiceFromWire(tc *AnthropicToolChoice) *schema.ToolChoice {
	if tc == nil || tc.Type == "" {
		return nil
	}
	switch tc.Type {
	case "auto":
		return &schema.ToolChoice{Mode: schema.ToolChoiceAuto}
	case "none":
		return &schema.ToolChoice{Mode: schema.ToolChoiceNone}
	case "any":
		return &schema.ToolChoice{Mode: schema.ToolChoiceRequired}
	case "tool":
		return &schema.ToolChoice{Mode: schema.ToolChoiceFunction, Function: tc.Name}
	}
	return nil
}

// anthropicToolChoiceToWire 渲染 IR tool_choice 为 wire 形。
func anthropicToolChoiceToWire(tc *schema.ToolChoice) *AnthropicToolChoice {
	if tc == nil {
		return nil
	}
	switch tc.Mode {
	case schema.ToolChoiceNone:
		return &AnthropicToolChoice{Type: "none"}
	case schema.ToolChoiceRequired:
		return &AnthropicToolChoice{Type: "any"}
	case schema.ToolChoiceFunction:
		return &AnthropicToolChoice{Type: "tool", Name: tc.Function}
	default: // auto 及未知
		return &AnthropicToolChoice{Type: "auto"}
	}
}

// anthropicMessageFromWire 把一条入站消息映射为 0..n 条 IR 消息：
// assistant 的 tool_use blocks 提为 ToolCalls；user 的 tool_result
// blocks 拆为独立 tool 角色消息（多工具结果各自成条）。
func anthropicMessageFromWire(m AnthropicMessage) []schema.Message {
	var out []schema.Message
	msg := schema.Message{Role: m.Role}
	var calls []schema.ToolCall
	for _, p := range m.Content.Parts {
		switch anthropicRawKind(p.Raw) {
		case "tool_use":
			if c, ok := toolCallFromRaw(p.Raw); ok {
				calls = append(calls, c)
			}
		case "tool_result":
			if tm, ok := toolMessageFromRaw(p.Raw); ok {
				out = append(out, tm)
			}
		default:
			msg.Content.Parts = append(msg.Content.Parts, p)
		}
	}
	msg.ToolCalls = calls
	if len(msg.Content.Parts) > 0 || len(calls) > 0 {
		out = append(out, msg)
	}
	return out
}

// anthropicRawKind 探测 Raw block 的 type；空 Raw 返回 ""。
func anthropicRawKind(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return ""
	}
	return head.Type
}

// toolCallFromRaw 解析 tool_use block 为 IR ToolCall（input 对象序列化
// 回字符串，保持 OpenAI arguments 口径）。
func toolCallFromRaw(raw json.RawMessage) (schema.ToolCall, bool) {
	var b toolUseBlock
	if err := json.Unmarshal(raw, &b); err != nil || b.Name == "" {
		return schema.ToolCall{}, false
	}
	input := b.Input
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	return schema.ToolCall{
		ID:   b.ID,
		Type: "function",
		Function: schema.ToolCallFunction{
			Name: b.Name, Arguments: string(input),
		},
	}, true
}

// toolMessageFromRaw 解析 tool_result block 为 IR tool 角色消息；
// content 兼容 string 与 blocks 数组两种形态（取文本）。
func toolMessageFromRaw(raw json.RawMessage) (schema.Message, bool) {
	var b struct {
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &b); err != nil || b.ToolUseID == "" {
		return schema.Message{}, false
	}
	return schema.Message{
		Role:       schema.RoleTool,
		ToolCallID: b.ToolUseID,
		Content:    schema.NewTextContent(rawTextOf(b.Content)),
	}, true
}

// rawTextOf 提取 string 或 [{type:text,text:…}] 形态的文本。
func rawTextOf(raw json.RawMessage) string {
	trimmed := raw
	for len(trimmed) > 0 && trimmed[0] == ' ' {
		trimmed = trimmed[1:]
	}
	if len(trimmed) == 0 {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if json.Unmarshal(trimmed, &s) == nil {
			return s
		}
		return ""
	}
	var blocks []AnthropicBlock
	if json.Unmarshal(trimmed, &blocks) == nil {
		out := ""
		for _, b := range blocks {
			out += b.Text
		}
		return out
	}
	return ""
}

// anthropicToolUsePart 渲染一条 IR tool_call 为 tool_use block
// （arguments 字符串解析回对象，无效补 {}）。
func anthropicToolUsePart(c schema.ToolCall) schema.Part {
	input := json.RawMessage(c.Function.Arguments)
	if len(input) == 0 || !json.Valid(input) {
		input = json.RawMessage("{}")
	}
	raw, _ := json.Marshal(toolUseBlock{
		Type: "tool_use", ID: c.ID, Name: c.Function.Name, Input: input,
	})
	return schema.Part{Type: "tool_use", Raw: raw}
}

// anthropicToolResultPart 渲染出站 tool 消息为 user 消息的
// tool_result block。
func anthropicToolResultPart(m schema.Message) (schema.Part, bool) {
	if m.ToolCallID == "" {
		return schema.Part{}, false
	}
	raw, _ := json.Marshal(toolResultBlock{
		Type:      "tool_result",
		ToolUseID: m.ToolCallID,
		Content:   m.Content.TextOf(),
	})
	return schema.Part{Type: "tool_result", Raw: raw}, true
}
