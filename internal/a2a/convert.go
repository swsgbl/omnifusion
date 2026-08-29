// convert.go 在 A2A 消息与网关中枢 IR（schema.UnifiedRequest）之间转换。
package a2a

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// ErrNoContent 表示消息没有任何可提取的内容片段。
var ErrNoContent = errors.New("a2a: message has no usable content parts")

// ToUnified 把 A2A user 消息翻译为中枢 IR：
//   - role：ROLE_USER→user，ROLE_AGENT→assistant，其余拒绝；
//   - 内容：text 片段直取；data 片段序列化为 JSON 文本；file 片段
//     以引用占位（v1 不上传二进制）；全部拼接为单条纯文本消息；
//   - 模型：message.metadata.model（可含 @smart/@fusion/@combo 指令），
//     缺省回落 defaultModel。
func ToUnified(m *Message, defaultModel string) (*schema.UnifiedRequest, error) {
	role := ""
	switch m.Role {
	case RoleUser:
		role = "user"
	case RoleAgent:
		role = "assistant"
	case "":
		return nil, fmt.Errorf("a2a: message role is required (ROLE_USER/ROLE_AGENT)")
	default:
		return nil, fmt.Errorf("a2a: unsupported message role %q", m.Role)
	}

	var sb strings.Builder
	for i := range m.Parts {
		p := &m.Parts[i]
		switch {
		case p.Text != "":
			sb.WriteString(p.Text)
		case len(p.Data) > 0:
			sb.Write(p.Data)
		case p.URL != "":
			_, _ = fmt.Fprintf(&sb, "\n[file: %s]", p.URL)
		case p.Raw != "":
			_, _ = fmt.Fprintf(&sb, "\n[file: %s (base64 omitted)]", p.Filename)
		}
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return nil, ErrNoContent
	}

	model := defaultModel
	var meta struct {
		Model string `json:"model"`
	}
	if len(m.Metadata) > 0 {
		if err := json.Unmarshal(m.Metadata, &meta); err != nil {
			return nil, fmt.Errorf("a2a: message.metadata: %v", err)
		}
		if meta.Model != "" {
			model = meta.Model
		}
	}

	req := &schema.UnifiedRequest{Model: model}
	req.Messages = []schema.Message{{
		Role:    role,
		Content: schema.Content{Parts: []schema.Part{{Type: schema.PartText, Text: text}}},
	}}
	// 会话亲和：调用方把 m.ContextID 映射为 routing.WithSession
	// （sticky 语义），此处不经 IR 携带。
	return req, nil
}

// FromResponse 把非流式聚合响应翻译为 A2A agent 消息：正文取首个
// choice 的文本部分，usage 进 metadata（agent 侧可见用量）。
func FromResponse(resp *schema.Response) *Message {
	msg := &Message{Role: RoleAgent}
	if resp != nil {
		msg.MessageID = "msg-" + resp.ID
		if text := responseText(resp); text != "" {
			msg.Parts = []Part{TextPart(text)}
		}
		if resp.Usage != nil {
			meta := map[string]any{
				"usage": map[string]int{
					"promptTokens":     resp.Usage.PromptTokens,
					"completionTokens": resp.Usage.CompletionTokens,
					"totalTokens":      resp.Usage.TotalTokens,
				},
				"model":    resp.Model,
				"provider": resp.ProviderName,
			}
			if b, err := json.Marshal(meta); err == nil {
				msg.Metadata = b
			}
		}
	}
	if len(msg.Parts) == 0 {
		msg.Parts = []Part{TextPart("")}
	}
	return msg
}

// responseText 提取响应全部文本部分（choice 0，保序拼接）。
func responseText(resp *schema.Response) string {
	var sb strings.Builder
	for _, ch := range resp.Choices {
		for _, p := range ch.Message.Content.Parts {
			if p.Type == schema.PartText {
				sb.WriteString(p.Text)
			}
		}
	}
	return sb.String()
}

// ChunkText 提取流式增量的文本部分（供 SSE 边界逐事件转译）。
func ChunkText(c *schema.Chunk) string {
	var sb strings.Builder
	for _, ch := range c.Choices {
		for _, p := range ch.Delta.Content.Parts {
			if p.Type == schema.PartText {
				sb.WriteString(p.Text)
			}
		}
	}
	return sb.String()
}
