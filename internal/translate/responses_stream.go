// responses_stream.go 把归一化 chunk 流编码为 Responses API SSE 事件
// 序列：response.created → output_item.added(message) → content_part.added
// → output_text.delta* → output_text.done → content_part.done →
// output_item.done → [function_call items] → response.completed。
// Codex CLI 按此序列消费；EOF 与断流都经 Finish 收尾（M3.4 优雅收尾的
// 入站侧）。帧形如 "event: <type>\ndata: <json>\n\n"。
package translate

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// ResponsesStreamEncoder 是有状态的流编码器（非并发安全）。
type ResponsesStreamEncoder struct {
	created   bool
	itemOpen  bool
	id        string
	model     string
	createdAt int64
	text      strings.Builder
	toolCalls map[int]*schema.ToolCall
	toolOrder []int
	usage     *schema.Usage
}

// NewResponsesStreamEncoder 装配编码器。
func NewResponsesStreamEncoder() *ResponsesStreamEncoder {
	return &ResponsesStreamEncoder{toolCalls: map[int]*schema.ToolCall{}}
}

// Feed 消费一个归一化 chunk，产出该批事件帧。
func (e *ResponsesStreamEncoder) Feed(c *schema.Chunk) [][]byte {
	var frames [][]byte
	if !e.created {
		e.created = true
		e.id, e.model, e.createdAt = responsesID(c.ID), c.Model, c.Created
		frames = append(frames, e.frame("response.created", map[string]any{
			"type": "response.created",
			"response": map[string]any{
				"id": e.id, "object": "response", "created_at": e.createdAt,
				"status": "in_progress", "model": e.model, "output": []any{},
			},
		}))
	}
	for _, ch := range c.Choices {
		frames = append(frames, e.feedChoice(ch)...)
	}
	if c.Usage != nil {
		u := *c.Usage
		e.usage = &u
	}
	return frames
}

// feedChoice 处理单条 choice：文本增量懒开 item；工具调用按 index 累积。
func (e *ResponsesStreamEncoder) feedChoice(ch schema.ChunkChoice) [][]byte {
	var frames [][]byte
	if txt := choiceDeltaText(ch); txt != "" {
		if !e.itemOpen {
			e.itemOpen = true
			frames = append(frames,
				e.frame("response.output_item.added", map[string]any{
					"type": "response.output_item.added", "output_index": 0,
					"item": map[string]any{
						"type": "message", "id": responsesMsgID(e.id), "role": "assistant",
						"status": "in_progress", "content": []any{},
					},
				}),
				e.frame("response.content_part.added", map[string]any{
					"type": "response.content_part.added", "item_id": responsesMsgID(e.id),
					"output_index": 0, "content_index": 0,
					"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
				}))
		}
		e.text.WriteString(txt)
		frames = append(frames, e.frame("response.output_text.delta", map[string]any{
			"type": "response.output_text.delta", "item_id": responsesMsgID(e.id),
			"output_index": 0, "content_index": 0, "delta": txt,
		}))
	}
	for _, tc := range ch.Delta.ToolCalls {
		idx := 0
		if tc.Index != nil {
			idx = *tc.Index
		}
		cur := e.toolCalls[idx]
		if cur == nil {
			cur = &schema.ToolCall{ID: tc.ID, Type: "function"}
			e.toolCalls[idx] = cur
			e.toolOrder = append(e.toolOrder, idx)
		}
		if tc.ID != "" {
			cur.ID = tc.ID
		}
		cur.Function.Name += tc.Function.Name
		cur.Function.Arguments += tc.Function.Arguments
	}
	return frames
}

// choiceDeltaText 取 choice 增量的纯文本。
func choiceDeltaText(ch schema.ChunkChoice) string {
	return ch.Delta.Content.TextOf()
}

// Finish 产出收尾帧：文本 item 关闭 → function_call items → completed。
func (e *ResponsesStreamEncoder) Finish() [][]byte {
	var frames [][]byte
	if !e.created { // 空流：至少给客户端完整的生命周期
		e.created = true
		e.id, e.createdAt = "resp_ofd", 0
		frames = append(frames, e.frame("response.created", map[string]any{
			"type": "response.created",
			"response": map[string]any{"id": e.id, "object": "response",
				"status": "in_progress", "model": e.model, "output": []any{}},
		}))
	}
	if e.itemOpen {
		full := e.text.String()
		frames = append(frames,
			e.frame("response.output_text.done", map[string]any{
				"type": "response.output_text.done", "item_id": responsesMsgID(e.id),
				"output_index": 0, "content_index": 0, "text": full,
			}),
			e.frame("response.content_part.done", map[string]any{
				"type": "response.content_part.done", "item_id": responsesMsgID(e.id),
				"output_index": 0, "content_index": 0,
				"part": map[string]any{"type": "output_text", "text": full, "annotations": []any{}},
			}),
			e.frame("response.output_item.done", map[string]any{
				"type": "response.output_item.done", "output_index": 0,
				"item": map[string]any{
					"type": "message", "id": responsesMsgID(e.id), "role": "assistant",
					"status": "completed",
					"content": []any{map[string]any{"type": "output_text", "text": full, "annotations": []any{}}},
				},
			}))
	}
	for i, idx := range e.toolOrder { // 工具调用整 item 下发（v1 无逐参 delta）
		tc := e.toolCalls[idx]
		item := map[string]any{
			"type": "function_call", "id": responsesCallID(tc.ID),
			"status": "completed", "call_id": tc.ID,
			"name": tc.Function.Name, "arguments": tc.Function.Arguments,
		}
		frames = append(frames,
			e.frame("response.output_item.added", map[string]any{
				"type": "response.output_item.added", "output_index": i + boolInt(e.itemOpen),
				"item": item,
			}),
			e.frame("response.output_item.done", map[string]any{
				"type": "response.output_item.done", "output_index": i + boolInt(e.itemOpen),
				"item": item,
			}))
	}
	frames = append(frames, e.frame("response.completed", map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id": e.id, "object": "response", "created_at": e.createdAt,
			"status": "completed", "model": e.model,
			"output": e.finalOutput(), "usage": e.finalUsage(),
		},
	}))
	return frames
}

// finalOutput 汇总 completed 事件里的 output 摘要。
func (e *ResponsesStreamEncoder) finalOutput() []any {
	var out []any
	if e.itemOpen {
		out = append(out, map[string]any{
			"type": "message", "id": responsesMsgID(e.id), "role": "assistant",
			"status": "completed",
			"content": []any{map[string]any{
				"type": "output_text", "text": e.text.String(), "annotations": []any{},
			}},
		})
	}
	for _, idx := range e.toolOrder {
		tc := e.toolCalls[idx]
		out = append(out, map[string]any{
			"type": "function_call", "id": responsesCallID(tc.ID),
			"status": "completed", "call_id": tc.ID,
			"name": tc.Function.Name, "arguments": tc.Function.Arguments,
		})
	}
	return out
}

// finalUsage 产出 completed 事件的用量（无 usage 时零值给全）。
func (e *ResponsesStreamEncoder) finalUsage() map[string]int {
	in, outv := 0, 0
	if e.usage != nil {
		in, outv = e.usage.PromptTokens, e.usage.CompletionTokens
	}
	return map[string]int{"input_tokens": in, "output_tokens": outv, "total_tokens": in + outv}
}

// boolInt 是小型布尔的整数化（输出索引偏移）。
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// frame 组一帧 SSE（event + data）。
func (e *ResponsesStreamEncoder) frame(event string, payload any) []byte {
	b, err := json.Marshal(payload)
	if err != nil {
		b = []byte(`{"type":"` + event + `"}`)
	}
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, b))
}
