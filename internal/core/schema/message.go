// Package schema 定义三协议互译的中枢 IR（UnifiedRequest/UnifiedResponse）。
// 形状以 OpenAI Chat Completions 为基准；任何方向翻译不支持的特性必须显式
// 降级并在响应中标记，禁止静默丢弃（见 docs/04-架构设计 §7）。
package schema

import "encoding/json"

// Role 取值（OpenAI 口径）。
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message 是一条会话消息。Content 支持纯字符串与多模态数组两种线上形态。
type Message struct {
	Role       string     `json:"role"`
	Content    Content    `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Refusal    string     `json:"refusal,omitempty"`
}

// Content 是消息正文：0 个或多个部分。空 Content 序列化为 JSON null
// （assistant 工具调用消息的合法形态）。
type Content struct {
	Parts []Part
}

// PartType 枚举内容部分类型。
type PartType string

// 已知内容部分类型。
const (
	PartText       PartType = "text"
	PartImageURL   PartType = "image_url"
	PartInputAudio PartType = "input_audio"
	PartFile       PartType = "file"
)

// Part 是内容的一个片段。未知类型保留原始 JSON 于 Raw 以供透传。
type Part struct {
	Type       PartType        `json:"type"`
	Text       string          `json:"-"`
	ImageURL   *ImageURL       `json:"-"`
	InputAudio *InputAudio     `json:"-"`
	File       *FilePart       `json:"-"`
	Raw        json.RawMessage `json:"-"`
}

// ImageURL 是 image_url 部分的载荷。
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// InputAudio 是 input_audio 部分的载荷。
type InputAudio struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

// FilePart 是 file 部分的载荷（文档类附件）。
type FilePart struct {
	FileData    string `json:"file_data,omitempty"`
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

// TextOf 返回 Content 的纯文本拼接（忽略非文本部分）。
func (c Content) TextOf() string {
	n := 0
	for i := range c.Parts {
		if c.Parts[i].Type == PartText {
			n += len(c.Parts[i].Text)
		}
	}
	buf := make([]byte, 0, n)
	for i := range c.Parts {
		if c.Parts[i].Type == PartText {
			buf = append(buf, c.Parts[i].Text...)
		}
	}
	return string(buf)
}

// NewTextContent 构造仅含一段文本的 Content。
func NewTextContent(text string) Content {
	return Content{Parts: []Part{{Type: PartText, Text: text}}}
}

// ExtraFields 承载未建模字段的透传（按出现顺序保留键）。
type ExtraFields struct {
	keys   []string
	values map[string]json.RawMessage
}

// Set 记录一个透传字段。
func (e *ExtraFields) Set(key string, raw json.RawMessage) {
	if e.values == nil {
		e.values = make(map[string]json.RawMessage)
	}
	if _, ok := e.values[key]; !ok {
		e.keys = append(e.keys, key)
	}
	e.values[key] = raw
}

// Keys 按插入顺序返回字段名。
func (e *ExtraFields) Keys() []string { return e.keys }

// Get 返回字段原始 JSON。
func (e *ExtraFields) Get(key string) (json.RawMessage, bool) {
	v, ok := e.values[key]
	return v, ok
}
