// stream.go 是 Gemini 上游的流式适配：alt=sse 的事件流里每条
// data 载荷都是一个完整的 GenerateContentResponse，归一为 IR chunk。
// 与 OpenAI 形不同，Gemini 流没有 [DONE] 终止符——连接干净关闭即正常
// 收尾（io.EOF），读错误仍归一为 StreamError 驱动首 chunk 前换家。
package gemini

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
		return nil, fmt.Errorf("gemini: %q nil upstream response", a.spec.ProviderName)
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

type sseStream struct {
	providerName string
	callModel    string
	body         io.ReadCloser
	sc           *bufio.Scanner
	data         []string // pending data lines of the current event
	id           string
	model        string
	nextTool     int  // 已见 functionCall 计数（作 IR tool_call index）
	sawTool      bool // 流内出现过 functionCall（STOP 不区分工具调用）
	closed       bool
}

// Next implements provider.ChunkStream.
func (s *sseStream) Next(ctx context.Context) (*schema.Chunk, error) {
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
			// Gemini 流无 [DONE]：干净读完就是正常收尾。
			return nil, io.EOF
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
			// event:/id:/retry: 行忽略
		}
	}
}

// dispatch 把一个 GenerateContentResponse 帧归一为 chunk（文本增量 /
// finishReason / usageMetadata 各拼进同一事件）。
func (s *sseStream) dispatch(payload string) (*schema.Chunk, error) {
	var frame translate.GeminiResponse
	if err := json.Unmarshal([]byte(payload), &frame); err != nil {
		return nil, &provider.StreamError{
			Provider: s.providerName,
			Reason:   provider.StreamDecode,
			Err:      err,
		}
	}
	s.id, s.model = frame.ResponseID, frame.ModelVersion
	c := schema.NewChunk(s.id, s.model, 0)
	c.ProviderName = s.providerName
	if c.Model == "" {
		c.Model = s.callModel
	}
	if len(frame.Candidates) > 0 {
		cand := &frame.Candidates[0]
		var sb strings.Builder
		var calls []schema.ToolCall
		for _, p := range cand.Content.Parts {
			sb.WriteString(p.Text)
			if p.FunctionCall != nil {
				idx := s.nextTool
				s.nextTool++
				s.sawTool = true
				tc := translate.ToolCallFromGemini(*p.FunctionCall)
				tc.Index = &idx
				calls = append(calls, tc)
			}
		}
		fin := ""
		if cand.FinishReason != "" {
			fin = translate.MapGeminiFinish(cand.FinishReason)
			if fin == schema.FinishStop && (len(calls) > 0 || s.sawTool) {
				fin = schema.FinishToolCalls // STOP 不区分工具调用
			}
		}
		if sb.Len() > 0 || cand.FinishReason != "" || len(calls) > 0 {
			c.Choices = append(c.Choices, schema.ChunkChoice{
				Delta: schema.Message{
					Content:   schema.NewTextContent(sb.String()),
					ToolCalls: calls,
				},
				FinishReason: fin,
			})
		}
	}
	if u := frame.UsageMetadata; u != nil &&
		(u.PromptTokenCount != 0 || u.CandidatesTokenCount != 0) {
		c.Usage = &schema.Usage{
			PromptTokens:     u.PromptTokenCount,
			CompletionTokens: u.CandidatesTokenCount,
			TotalTokens:      u.TotalTokenCount,
		}
	}
	if len(c.Choices) == 0 && c.Usage == nil {
		return nil, nil // 空帧（如纯 role 帧）不产出事件
	}
	return c, nil
}

// Close implements provider.ChunkStream.
func (s *sseStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.body.Close()
}
