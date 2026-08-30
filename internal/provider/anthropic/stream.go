// stream.go 是 Anthropic 上游的流式适配：把 Messages SSE 事件
// 序列归一为 IR chunk 流。状态机按 data JSON 的 type 字段分派——
// message_start → 基础 chunk（role + input usage）；content_block_delta
// text_delta → 文本增量；message_delta → finish + output usage；
// message_stop → io.EOF；error → Status==0 的 UpstreamError（流内错误
// 无 HTTP 状态码可用，路由层按 stream_broken 分类）。
package anthropic

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/translate"
)

// maxSSELine bounds one SSE line (same rationale as openai_compat).
const maxSSELine = 1 << 20 // 1 MiB

// StreamClient implements provider.StreamParser.
func (a *Adapter) StreamClient() *http.Client { return a.streamClient }

// ParseStream implements provider.StreamParser.
func (a *Adapter) ParseStream(ctx context.Context, call *provider.ProviderCall, resp *http.Response) (provider.ChunkStream, error) {
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("anthropic: %q nil upstream response", a.spec.ProviderName)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer func() { _ = resp.Body.Close() }()
		return nil, readUpstreamError(a.spec.ProviderName, resp.StatusCode, resp.Body)
	}
	model := ""
	if call != nil {
		model = call.Model
	}
	return &sseStream{
		providerName: a.spec.ProviderName,
		callModel:    model,
		body:         resp.Body,
		sc:           newStreamScanner(resp.Body),
	}, nil
}

func newStreamScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxSSELine)
	return sc
}

// sseEvent 是流事件的宽松解码形：只取状态机关心的字段。
type sseEvent struct {
	Type         string                    `json:"type"`
	Index        *int                      `json:"index"`
	Message      sseMessage                `json:"message"`
	Delta        sseDelta                  `json:"delta"`
	ContentBlock *sseContentBlock          `json:"content_block"`
	Usage        *translate.AnthropicUsage `json:"usage"`
	Error        json.RawMessage           `json:"error"`
}

// sseMessage 承载 message_start 里的消息骨架。
type sseMessage struct {
	ID    string                    `json:"id"`
	Model string                    `json:"model"`
	Role  string                    `json:"role"`
	Usage *translate.AnthropicUsage `json:"usage"`
}

// sseContentBlock 承载 content_block_start 的块骨架（tool_use 专属）。
type sseContentBlock struct {
	Type string `json:"type"` // text / tool_use
	ID   string `json:"id"`
	Name string `json:"name"`
}

// sseDelta 承载 content_block_delta / message_delta 的增量。
type sseDelta struct {
	Type        string `json:"type"` // text_delta / input_json_delta / …
	Text        string `json:"text"`
	PartialJSON string `json:"partial_json"`
	StopReason  string `json:"stop_reason"`
}

type sseStream struct {
	providerName string
	callModel    string
	body         io.ReadCloser
	sc           *bufio.Scanner
	data         []string // pending data lines of the current event
	id           string
	model        string
	toolIdx      map[int]int // anthropic 块 index → IR tool_call index
	nextTool     int
	done         bool
	closed       bool
}

// Next implements provider.ChunkStream.
func (s *sseStream) Next(ctx context.Context) (*schema.Chunk, error) {
	if s.done {
		return nil, io.EOF
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !s.sc.Scan() {
			if err := s.sc.Err(); err != nil {
				return nil, &provider.StreamError{
					Provider: s.providerName,
					Reason:   provider.StreamRead,
					Err:      err,
				}
			}
			// Anthropic 流必以 message_stop 收尾：未见即关闭视为断流
			// （驱动首 chunk 前的换家重试）。
			return nil, &provider.StreamError{
				Provider: s.providerName,
				Reason:   provider.StreamEndedWithoutDone,
				Err:      io.ErrUnexpectedEOF,
			}
		}
		line := s.sc.Text()
		switch {
		case line == "":
			if len(s.data) == 0 {
				continue // bare keep-alive boundary
			}
			payload := strings.TrimSpace(strings.Join(s.data, "\n"))
			s.data = s.data[:0]
			if payload == "" {
				continue
			}
			if chunk, err := s.dispatch(payload); chunk != nil || err != nil {
				return chunk, err
			}
		case strings.HasPrefix(line, ":"):
			// comment / keep-alive line
		case strings.HasPrefix(line, "data:"):
			s.data = append(s.data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			// event:/id:/retry: 行与 data 语义重复（type 在载荷里），忽略
		}
	}
}

// dispatch 消费一条完整事件载荷，返回产出 chunk / 终止错误 / nil 继续。
func (s *sseStream) dispatch(payload string) (*schema.Chunk, error) {
	var ev sseEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		return nil, &provider.StreamError{
			Provider: s.providerName,
			Reason:   provider.StreamDecode,
			Err:      err,
		}
	}
	switch ev.Type {
	case "message_start":
		s.id, s.model = ev.Message.ID, ev.Message.Model
		return s.chunk(func(c *schema.Chunk) {
			c.Choices = append(c.Choices, schema.ChunkChoice{
				Delta: schema.Message{Role: schema.RoleAssistant},
			})
			if u := ev.Message.Usage; u != nil && u.InputTokens > 0 {
				c.Usage = &schema.Usage{PromptTokens: u.InputTokens}
			}
		}), nil
	case "content_block_start":
		return s.toolCallStart(&ev)
	case "content_block_delta":
		if tc, ok := s.toolArgsDelta(&ev); ok {
			return s.chunk(func(c *schema.Chunk) {
				c.Choices = append(c.Choices, schema.ChunkChoice{
					Delta: schema.Message{ToolCalls: []schema.ToolCall{tc}},
				})
			}), nil
		}
		if ev.Delta.Text == "" {
			return nil, nil // 其他块型增量暂无 IR 对应
		}
		return s.chunk(func(c *schema.Chunk) {
			c.Choices = append(c.Choices, schema.ChunkChoice{
				Delta: schema.Message{Content: schema.NewTextContent(ev.Delta.Text)},
			})
		}), nil
	case "message_delta":
		return s.chunk(func(c *schema.Chunk) {
			c.Choices = append(c.Choices, schema.ChunkChoice{
				FinishReason: translate.MapAnthropicStop(ev.Delta.StopReason),
			})
			if u := ev.Usage; u != nil && u.OutputTokens > 0 {
				c.Usage = &schema.Usage{CompletionTokens: u.OutputTokens}
			}
		}), nil
	case "message_stop":
		s.done = true
		return nil, io.EOF
	case "error":
		return nil, &provider.UpstreamError{
			Provider: s.providerName,
			Status:   0, // mid-stream error event; no HTTP status applies
			Body:     truncatePayload(payload),
		}
	default:
		// ping / content_block_stop：无载荷语义
		return nil, nil
	}
}

// toolCallStart 处理 content_block_start：tool_use 块登记 block→IR
// index 映射并产出携带 id/name 的首段 tool_call 增量；text 块无产出。
func (s *sseStream) toolCallStart(ev *sseEvent) (*schema.Chunk, error) {
	cb := ev.ContentBlock
	if cb == nil || cb.Type != "tool_use" || ev.Index == nil {
		return nil, nil
	}
	ir := s.nextTool
	s.nextTool++
	if s.toolIdx == nil {
		s.toolIdx = map[int]int{}
	}
	s.toolIdx[*ev.Index] = ir
	idx := ir
	return s.chunk(func(c *schema.Chunk) {
		c.Choices = append(c.Choices, schema.ChunkChoice{
			Delta: schema.Message{ToolCalls: []schema.ToolCall{{
				ID: cb.ID, Type: "function", Index: &idx,
				Function: schema.ToolCallFunction{Name: cb.Name},
			}}},
		})
	}), nil
}

// toolArgsDelta 处理 input_json_delta：把 partial_json 作为 arguments
// 增量转发（OpenAI 流式口径），块 index 换算回 IR tool_call index。
func (s *sseStream) toolArgsDelta(ev *sseEvent) (schema.ToolCall, bool) {
	if ev.Delta.Type != "input_json_delta" || ev.Delta.PartialJSON == "" || ev.Index == nil {
		return schema.ToolCall{}, false
	}
	ir, ok := s.toolIdx[*ev.Index]
	if !ok {
		return schema.ToolCall{}, false // 未见 start 的块，跳过
	}
	idx := ir
	return schema.ToolCall{
		Index:    &idx,
		Function: schema.ToolCallFunction{Arguments: ev.Delta.PartialJSON},
	}, true
}

// chunk 以当前会话标识（id/model）装配一个归一化事件。
func (s *sseStream) chunk(fill func(*schema.Chunk)) *schema.Chunk {
	c := schema.NewChunk(s.id, s.model, 0)
	c.ProviderName = s.providerName
	if c.Model == "" {
		c.Model = s.callModel
	}
	fill(c)
	return c
}

// Close implements provider.ChunkStream.
func (s *sseStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.body.Close()
}

// truncatePayload bounds the error body retained for logging.
func truncatePayload(p string) []byte {
	const n = 512
	if len(p) <= n {
		return []byte(p)
	}
	return []byte(p[:n] + "...")
}
