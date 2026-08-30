// Package translate 实现三协议互译的纯函数对（ 翻译矩阵），
// 以 schema.UnifiedRequest/Response 为中枢 IR。任何方向翻译不支持的
// 特性必须显式降级并在响应中标记（FromAnthropicMessages 返回降级清单），
// 禁止静默丢弃。 先落 Anthropic Messages 入站两个方向。
package translate

import (
	"encoding/json"
	"strings"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// AnthropicRequest 是 Anthropic Messages API（/v1/messages）请求体。
// content/system 复用 schema.Content 解码（string 与 blocks 数组两态），
// 未建模的 block 类型经 Part.Raw 原样保留在 IR 中透传。
type AnthropicRequest struct {
	Model         string               `json:"model"`
	MaxTokens     int                  `json:"max_tokens"`
	Messages      []AnthropicMessage   `json:"messages"`
	System        schema.Content       `json:"system"`
	Temperature   *float64             `json:"temperature,omitempty"`
	TopP          *float64             `json:"top_p,omitempty"`
	TopK          *int                 `json:"top_k,omitempty"`
	StopSequences []string             `json:"stop_sequences,omitempty"`
	Stream        bool                 `json:"stream,omitempty"`
	Metadata      json.RawMessage      `json:"metadata,omitempty"`
	Tools         []AnthropicTool      `json:"tools,omitempty"`       // 工具互译
	ToolChoice    *AnthropicToolChoice `json:"tool_choice,omitempty"` // 
}

// AnthropicMessage 是一条会话消息（role 仅 user/assistant）。
type AnthropicMessage struct {
	Role    string         `json:"role"`
	Content schema.Content `json:"content"`
}

// FromAnthropicMessages 把入站请求归一化为中枢 IR：顶层 system 展开
// 为首条 system 消息；max_tokens/stop_sequences 等映射到同义字段。
// 无 IR 对应的字段记入返回的降级清单（调用方标记进响应），不静默丢。
func FromAnthropicMessages(in *AnthropicRequest) (*schema.UnifiedRequest, []string) {
	req := &schema.UnifiedRequest{
		Model:       in.Model,
		Stream:      in.Stream,
		Temperature: in.Temperature,
		TopP:        in.TopP,
		Stop:        in.StopSequences,
	}
	if in.MaxTokens > 0 {
		mt := in.MaxTokens
		req.MaxTokens = &mt
	}
	if len(in.System.Parts) > 0 {
		req.Messages = append(req.Messages, schema.Message{Role: schema.RoleSystem, Content: in.System})
	}
	for _, m := range in.Messages {
		req.Messages = append(req.Messages, anthropicMessageFromWire(m)...)
	}
	req.Tools = anthropicToolsFromWire(in.Tools)
	req.ToolChoice = anthropicToolChoiceFromWire(in.ToolChoice)

	var degraded []string
	if in.TopK != nil {
		degraded = append(degraded, "top_k")
	}
	if len(in.Metadata) > 0 && string(in.Metadata) != "null" {
		degraded = append(degraded, "metadata")
	}
	return req, degraded
}

// AnthropicResponse 是 Anthropic Messages API 响应体。
type AnthropicResponse struct {
	ID           string           `json:"id"`
	Type         string           `json:"type"` // "message"
	Role         string           `json:"role"` // "assistant"
	Model        string           `json:"model"`
	Content      []AnthropicBlock `json:"content"`
	StopReason   string           `json:"stop_reason"`
	StopSequence *string          `json:"stop_sequence"`
	Usage        AnthropicUsage   `json:"usage"`
}

// AnthropicBlock 是响应内容块：text 或 tool_use。
type AnthropicBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// tool_use 专属：input 是参数对象。
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// AnthropicUsage 是 Anthropic 口径的用量（字段名与 OpenAI 不同）。
type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ToAnthropicMessages 把中枢聚合响应渲染为 Anthropic Messages 形态。
// 文本 part 逐段映射为独立 text 块；finish_reason 映射见 mapStopReason。
func ToAnthropicMessages(resp *schema.Response) *AnthropicResponse {
	out := &AnthropicResponse{
		ID:    anthropicID(resp.ID),
		Type:  "message",
		Role:  "assistant",
		Model: resp.Model,
	}
	for _, ch := range resp.Choices {
		for _, p := range ch.Message.Content.Parts {
			if p.Type == schema.PartText {
				out.Content = append(out.Content, AnthropicBlock{Type: "text", Text: p.Text})
			}
		}
		for _, c := range ch.Message.ToolCalls {
			input := json.RawMessage(c.Function.Arguments)
			if len(input) == 0 || !json.Valid(input) {
				input = json.RawMessage("{}")
			}
			out.Content = append(out.Content, AnthropicBlock{
				Type: "tool_use", ID: c.ID, Name: c.Function.Name, Input: input,
			})
		}
		if ch.FinishReason != "" {
			out.StopReason = MapStopReason(ch.FinishReason)
		}
	}
	if out.Content == nil {
		out.Content = []AnthropicBlock{}
	}
	if out.StopReason == "" {
		out.StopReason = "end_turn"
	}
	if resp.Usage != nil {
		out.Usage = AnthropicUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		}
	}
	return out
}

// anthropicID 保证线上 id 形如 msg_…（上游 id 透传，缺前缀则补）。
func anthropicID(id string) string {
	if id == "" {
		return "msg_ofd"
	}
	if strings.HasPrefix(id, "msg_") {
		return id
	}
	return "msg_" + id
}

// MapStopReason 把 OpenAI finish_reason 映射为 Anthropic stop_reason。
func MapStopReason(finish string) string {
	switch finish {
	case schema.FinishLength:
		return "max_tokens"
	case schema.FinishToolCalls:
		return "tool_use"
	case schema.FinishContentFilt:
		return "refusal"
	default: // stop 及未知值按正常收尾
		return "end_turn"
	}
}
