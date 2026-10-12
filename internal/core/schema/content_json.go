package schema

import (
	"encoding/json"
	"fmt"
)

// MarshalJSON 把 Content 编码为线上形态：
// 空 → null；单文本 → 字符串；否则 → 部分数组。
func (c Content) MarshalJSON() ([]byte, error) {
	if len(c.Parts) == 0 {
		return []byte("null"), nil
	}
	if len(c.Parts) == 1 && c.Parts[0].Type == PartText {
		return json.Marshal(c.Parts[0].Text)
	}
	return json.Marshal(c.Parts)
}

// UnmarshalJSON 接受 null / 字符串 / 数组三种线上形态。
func (c *Content) UnmarshalJSON(data []byte) error {
	trimmed := skipSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		c.Parts = nil
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		c.Parts = []Part{{Type: PartText, Text: s}}
		return nil
	}
	if trimmed[0] == '[' {
		var parts []Part
		if err := json.Unmarshal(data, &parts); err != nil {
			return err
		}
		c.Parts = parts
		return nil
	}
	return fmt.Errorf("schema: unexpected content form: %.20s", trimmed)
}

func skipSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\r' || b[0] == '\n') {
		b = b[1:]
	}
	return b
}

// reasoningSpellings 是思考内容在野外的四种拼写，捕获优先级即序
// （OpenRouter reasoning → DeepSeek reasoning_content → 其余）。
var reasoningSpellings = []string{"reasoning", "reasoning_content", "reasoning_text", "thinking"}

// messageWire 是 Message 的线上形态别名：不带自定义方法，避免
// (Un)marshalJSON 无限递归；Reasoning 由外层手工拼入。
type messageWire struct {
	Role       string     `json:"role"`
	Content    Content    `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Refusal    string     `json:"refusal,omitempty"`
}

// MarshalJSON 按默认形态编码，并在有思考内容时以捕获到的拼写补写
// 一个字段（拼接而非 map 重排，保持字段顺序稳定，流式热路径零重排）。
func (m Message) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(messageWire{
		Role: m.Role, Content: m.Content, Name: m.Name,
		ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, Refusal: m.Refusal,
	})
	if err != nil {
		return nil, err
	}
	if m.Reasoning == "" {
		return base, nil
	}
	key := m.ReasoningKey
	if key == "" {
		key = reasoningSpellings[0]
	}
	val, err := json.Marshal(m.Reasoning)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(base)+len(key)+len(val)+4)
	out = append(out, base[:len(base)-1]...) // 去掉收尾 '}'
	out = append(out, ',')
	out = append(out, '"')
	out = append(out, key...)
	out = append(out, '"', ':')
	out = append(out, val...)
	out = append(out, '}')
	return out, nil
}

// UnmarshalJSON 解码默认形态并捕获思考内容：四种拼写取第一个非空
// （部分网关同帧双报同一文本，全读会双倍计数）。请求方向（assistant
// 历史携带思考）与响应方向对称生效。
func (m *Message) UnmarshalJSON(data []byte) error {
	var w messageWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	m.Role, m.Content = w.Role, w.Content
	m.Name, m.ToolCalls, m.ToolCallID, m.Refusal = w.Name, w.ToolCalls, w.ToolCallID, w.Refusal
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil // 非对象形态（理论上不可达）：默认字段已就位
	}
	for _, key := range reasoningSpellings {
		raw, ok := probe[key]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil || s == "" {
			continue
		}
		m.Reasoning, m.ReasoningKey = s, key
		break
	}
	return nil
}

// MarshalJSON 按类型展开 Part。
func (p Part) MarshalJSON() ([]byte, error) {
	if len(p.Raw) > 0 {
		return p.Raw, nil
	}
	switch p.Type {
	case PartText:
		return json.Marshal(struct {
			Type PartType `json:"type"`
			Text string   `json:"text"`
		}{p.Type, p.Text})
	case PartImageURL:
		return json.Marshal(struct {
			Type     PartType  `json:"type"`
			ImageURL *ImageURL `json:"image_url"`
		}{p.Type, p.ImageURL})
	case PartInputAudio:
		return json.Marshal(struct {
			Type       PartType    `json:"type"`
			InputAudio *InputAudio `json:"input_audio"`
		}{p.Type, p.InputAudio})
	case PartFile:
		return json.Marshal(struct {
			Type PartType  `json:"type"`
			File *FilePart `json:"file"`
		}{p.Type, p.File})
	default:
		return json.Marshal(struct {
			Type PartType `json:"type"`
		}{p.Type})
	}
}

// UnmarshalJSON 按 type 分派解析；未知类型保留原始 JSON 于 Raw。
func (p *Part) UnmarshalJSON(data []byte) error {
	var head struct {
		Type       PartType        `json:"type"`
		Text       string          `json:"text"`
		ImageURL   *ImageURL       `json:"image_url"`
		InputAudio *InputAudio     `json:"input_audio"`
		File       *FilePart       `json:"file"`
		Raw        json.RawMessage `json:"-"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return err
	}
	p.Type = head.Type
	switch head.Type {
	case PartText:
		p.Text = head.Text
	case PartImageURL:
		p.ImageURL = head.ImageURL
	case PartInputAudio:
		p.InputAudio = head.InputAudio
	case PartFile:
		p.File = head.File
	default:
		p.Raw = append(json.RawMessage(nil), data...)
	}
	return nil
}
