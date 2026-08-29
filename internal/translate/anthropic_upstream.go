// anthropic_upstream.go 是矩阵的 Anthropic 上游列出站对（M3.2）：
// UnifiedRequest → AnthropicRequest（上游 wire），AnthropicResponse →
// UnifiedResponse。与入站对（anthropic.go）共用同一套 wire 类型，
// 方向相反语义互补，构成"Anthropic 直通"格的两侧。
package translate

import (
	"encoding/json"
	"strings"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// defaultAnthropicMaxTokens 是 IR 未设 max_tokens 时的出站默认
// （Anthropic 线上必填，宁可给保守值也不凭空失败）。
const defaultAnthropicMaxTokens = 4096

// ToAnthropicUpstreamRequest 把 IR 翻译为 Anthropic Messages 请求：
// system 消息抽到顶层 system；文本 part 直接映射；图片 part 映射为
// Anthropic image block（data-URI→base64 source，http(s)→url source，
// 经 Part.Raw 透传原形）；assistant 的 tool_calls 转为 tool_use
// blocks、tool 角色消息转为 user 消息内的 tool_result block（M3.3）。
// response_format 无原生对应（Messages API 不支持结构化输出），
// 丢弃并进 degraded 清单（M3.6：显式降级标记，禁止静默丢弃）。
func ToAnthropicUpstreamRequest(req *schema.UnifiedRequest) (*AnthropicRequest, []string) {
	out := &AnthropicRequest{
		Model:         req.Model,
		Stream:        req.Stream,
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		StopSequences: req.Stop,
		MaxTokens:     defaultAnthropicMaxTokens,
		Tools:         anthropicToolsToWire(req.Tools),
		ToolChoice:    anthropicToolChoiceToWire(req.ToolChoice),
	}
	var degraded []string
	if len(req.ResponseFormat) > 0 {
		degraded = append(degraded, "response_format")
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		out.MaxTokens = *req.MaxTokens
	}
	for _, m := range req.Messages {
		switch m.Role {
		case schema.RoleSystem:
			out.System.Parts = append(out.System.Parts, m.Content.Parts...)
		case schema.RoleTool:
			if p, ok := anthropicToolResultPart(m); ok {
				out.Messages = append(out.Messages, AnthropicMessage{
					Role: schema.RoleUser, Content: schema.Content{Parts: []schema.Part{p}}})
			}
		default:
			out.Messages = append(out.Messages, AnthropicMessage{
				Role: m.Role, Content: anthropicBlocks(m),
			})
		}
	}
	return out, degraded
}

// anthropicBlocks 把 IR 消息正文映射为 Anthropic blocks 形态：
// 文本 part 保持；图片 part 转 Anthropic image block（藏进 Part.Raw，
// 借既有透传机制按原样序列化）；tool_calls 追加 tool_use blocks。
func anthropicBlocks(m schema.Message) schema.Content {
	var out schema.Content
	for _, p := range m.Content.Parts {
		switch p.Type {
		case schema.PartText:
			out.Parts = append(out.Parts, p)
		case schema.PartImageURL:
			if raw, ok := anthropicImageBlock(p.ImageURL); ok {
				out.Parts = append(out.Parts, schema.Part{Type: "image", Raw: raw})
			}
		}
	}
	for _, c := range m.ToolCalls {
		out.Parts = append(out.Parts, anthropicToolUsePart(c))
	}
	if len(out.Parts) == 0 {
		out.Parts = append(out.Parts, schema.Part{Type: schema.PartText})
	}
	return out
}

// anthropicImageBlock 把 IR image_url 映射为 Anthropic image source。
func anthropicImageBlock(iu *schema.ImageURL) (json.RawMessage, bool) {
	if iu == nil || iu.URL == "" {
		return nil, false
	}
	if mime, data, ok := splitDataURI(iu.URL); ok {
		raw, _ := json.Marshal(map[string]any{
			"type": "image",
			"source": map[string]any{
				"type": "base64", "media_type": mime, "data": data,
			},
		})
		return raw, true
	}
	if strings.HasPrefix(iu.URL, "http") {
		raw, _ := json.Marshal(map[string]any{
			"type":   "image",
			"source": map[string]string{"type": "url", "url": iu.URL},
		})
		return raw, true
	}
	return nil, false
}

// splitDataURI 拆 "data:mime;base64,payload"。
func splitDataURI(uri string) (mime, data string, ok bool) {
	const prefix = "data:"
	if !strings.HasPrefix(uri, prefix) {
		return "", "", false
	}
	rest := uri[len(prefix):]
	semi := strings.Index(rest, ";base64,")
	if semi < 0 {
		return "", "", false
	}
	return rest[:semi], rest[semi+len(";base64,"):], true
}

// FromAnthropicUpstreamResponse 把上游 Anthropic 聚合响应归一为 IR。
func FromAnthropicUpstreamResponse(resp *AnthropicResponse) *schema.Response {
	out := &schema.Response{
		ID:      strings.TrimPrefix(resp.ID, "msg_"),
		Object:  "chat.completion",
		Model:   resp.Model,
		Choices: []schema.ResponseChoice{{}},
	}
	msg := &out.Choices[0].Message
	msg.Role = schema.RoleAssistant
	for _, b := range resp.Content {
		switch {
		case b.Type == "text":
			msg.Content.Parts = append(msg.Content.Parts,
				schema.Part{Type: schema.PartText, Text: b.Text})
		case b.Type == "tool_use" && b.ID != "" && b.Name != "":
			input := b.Input
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
				ID: b.ID, Type: "function",
				Function: schema.ToolCallFunction{
					Name: b.Name, Arguments: string(input),
				},
			})
		}
	}
	out.Choices[0].FinishReason = MapAnthropicStop(resp.StopReason)
	if resp.Usage.InputTokens != 0 || resp.Usage.OutputTokens != 0 {
		out.Usage = &schema.Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		}
	}
	return out
}

// MapAnthropicStop 把 Anthropic stop_reason 反映射为 OpenAI finish_reason。
func MapAnthropicStop(stop string) string {
	switch stop {
	case "max_tokens":
		return schema.FinishLength
	case "tool_use":
		return schema.FinishToolCalls
	case "refusal":
		return schema.FinishContentFilt
	default: // end_turn / stop_sequence / 空
		return schema.FinishStop
	}
}
