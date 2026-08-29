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
