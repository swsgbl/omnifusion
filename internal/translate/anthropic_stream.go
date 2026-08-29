// anthropic_stream.go 把归一化 chunk 流编码为 Anthropic SSE 事件序列：
// message_start → content_block_start → ping → content_block_delta* →
// content_block_stop → message_delta(stop_reason/usage) → message_stop。
// Claude Code 按此序列消费流；EOF 与断流都必须经 Finish 收尾，让客户端
// 拿到完整（即便部分）的消息而非悬挂连接（M3.4 优雅收尾的入站侧）。
package translate

import (
	"encoding/json"
	"fmt"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// AnthropicStreamEncoder 是有状态的流编码器（非并发安全）。
// Feed 逐 chunk 调用，返回该步应写出的事件帧（可为空）；流终结时
// 调 Finish 取收尾帧。帧形如 "event: <type>\ndata: <json>\n\n"。
type AnthropicStreamEncoder struct {
	started     bool
	inputTokens int
	outputChars int    // 无 usage 时的 output_tokens 字符估算
	usageOut    int    // 末块 usage 的权威值（>0 时优先）
	finish      string // 空=未见到 finish_reason
	id          string // 首 chunk 的上游 id/model，落进 message_start
	model       string
	nextIndex   int         // 下一个块 index（text 占 0，工具块从 1 递增）
	toolBlocks  map[int]int // 有 index 的流：IR tool_call index → 块 index
	nilLast     int         // 无 index 流最近的工具块 index（-1=无）
	openIdx     []int       // 已开工具块 index 序列（Finish 时关闭）
}

// NewAnthropicStreamEncoder 装配编码器。
func NewAnthropicStreamEncoder() *AnthropicStreamEncoder {
	return &AnthropicStreamEncoder{nextIndex: 1, nilLast: -1}
}

// Feed 消费一个归一化 chunk，产出该批事件帧。
func (e *AnthropicStreamEncoder) Feed(c *schema.Chunk) [][]byte {
	var frames [][]byte
	if !e.started {
		e.started = true
		e.id, e.model = anthropicID(c.ID), c.Model
		if c.Usage != nil {
			e.inputTokens = c.Usage.PromptTokens
		}
		frames = append(frames,
			e.frame("message_start", map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": e.id, "type": "message", "role": "assistant", "model": e.model,
					"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
					"usage": AnthropicUsage{InputTokens: e.inputTokens},
				},
			}),
			e.frame("content_block_start", map[string]any{
				"type": "content_block_start", "index": 0,
				"content_block": AnthropicBlock{Type: "text"},
			}),
			e.frame("ping", map[string]any{"type": "ping"}),
		)
	}
	for _, ch := range c.Choices {
		if text := ch.Delta.Content.TextOf(); text != "" {
			e.outputChars += len([]rune(text))
			frames = append(frames, e.frame("content_block_delta", map[string]any{
				"type": "content_block_delta", "index": 0,
				"delta": map[string]any{"type": "text_delta", "text": text},
			}))
		}
		for _, tc := range ch.Delta.ToolCalls {
			frames = append(frames, e.toolFrames(tc)...)
		}
		if ch.FinishReason != "" {
			e.finish = MapStopReason(ch.FinishReason)
		}
	}
	if c.Usage != nil && c.Usage.CompletionTokens > e.usageOut {
		e.usageOut = c.Usage.CompletionTokens
		if c.Usage.PromptTokens > e.inputTokens {
			e.inputTokens = c.Usage.PromptTokens
		}
	}
	return frames
}

// toolFrames 把 delta.tool_call 编码为 tool_use 块事件：首见发
// content_block_start（input 固定 {}，参数经 input_json_delta 流入），
// 后续片段只发 input_json_delta。
func (e *AnthropicStreamEncoder) toolFrames(tc schema.ToolCall) [][]byte {
	idx, seen := e.toolBlockIdx(tc)
	var frames [][]byte
	if !seen {
		idx = e.nextIndex
		e.nextIndex++
		e.openIdx = append(e.openIdx, idx)
		if tc.Index != nil {
			if e.toolBlocks == nil {
				e.toolBlocks = map[int]int{}
			}
			e.toolBlocks[*tc.Index] = idx
		}
		e.nilLast = idx
		frames = append(frames, e.frame("content_block_start", map[string]any{
			"type": "content_block_start", "index": idx,
			"content_block": toolUseBlock{
				Type: "tool_use", ID: tc.ID, Name: tc.Function.Name,
				Input: json.RawMessage("{}"),
			},
		}))
	}
	if args := tc.Function.Arguments; args != "" {
		frames = append(frames, e.frame("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": idx,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": args},
		}))
	}
	return frames
}

// toolBlockIdx 定位该 tool_call 已开的块 index；无 index 的流按
// "带 id/name 是新调用、纯参数片段续最近块" 启发式识别。
func (e *AnthropicStreamEncoder) toolBlockIdx(tc schema.ToolCall) (int, bool) {
	if tc.Index != nil {
		idx, ok := e.toolBlocks[*tc.Index]
		return idx, ok
	}
	if tc.ID == "" && tc.Function.Name == "" && e.nilLast >= 0 {
		return e.nilLast, true
	}
	return 0, false
}

// Finish 产出收尾帧：block 停止、stop_reason/用量汇总、message_stop。
// 未见过 finish_reason 按 end_turn 收尾（断流也走这里，不悬挂客户端）。
func (e *AnthropicStreamEncoder) Finish() [][]byte {
	if !e.started {
		e.started = true
		e.id = "msg_ofd_empty"
		return [][]byte{
			e.frame("message_start", map[string]any{"type": "message_start", "message": map[string]any{
				"id": e.id, "type": "message", "role": "assistant", "model": e.model,
				"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
				"usage": AnthropicUsage{},
			}}),
			e.frame("content_block_start", map[string]any{"type": "content_block_start", "index": 0,
				"content_block": AnthropicBlock{Type: "text"}}),
		}
	}
	stop := e.finish
	if stop == "" {
		stop = "end_turn"
	}
	tokens := e.usageOut
	if tokens == 0 {
		tokens = e.outputChars // 无 usage 的上游按字符估算
	}
	frames := [][]byte{
		e.frame("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}),
	}
	for _, idx := range e.openIdx {
		frames = append(frames, e.frame(
			"content_block_stop", map[string]any{"type": "content_block_stop", "index": idx}))
	}
	frames = append(frames,
		e.frame("message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
			"usage": AnthropicUsage{OutputTokens: tokens},
		}),
		e.frame("message_stop", map[string]any{"type": "message_stop"}),
	)
	return frames
}

// frame 拼一帧 SSE（event 行 + data 行 + 空行定界）。
func (e *AnthropicStreamEncoder) frame(event string, payload any) []byte {
	data, err := json.Marshal(payload)
	if err != nil {
		// payload 全是受控 map/struct，Marshal 不应失败；兜底防断流。
		data = []byte(fmt.Sprintf(`{"type":%q}`, event))
	}
	buf := make([]byte, 0, len(event)+len(data)+24)
	buf = append(buf, "event: "...)
	buf = append(buf, event...)
	buf = append(buf, "\ndata: "...)
	buf = append(buf, data...)
	return append(buf, "\n\n"...)
}
