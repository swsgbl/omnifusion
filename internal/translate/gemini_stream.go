// gemini_stream.go 把归一化 chunk 流编码为 Gemini SSE 流（每个
// GenerateContentResponse 一帧，"data: {json}\r\n\r\n" 与 Google 线上
// 形一致）。Gemini 流没有 [DONE] 终止符，正常由连接关闭收尾；本编码器
// 保证 finishReason 一定发出（未见 finish 的断流也补 STOP 优雅收尾）。
// 工具调用：Gemini 每帧 parts 均为完整形、无片段语义，IR 侧
// arguments 碎片先按调用缓冲，收尾时拼成完整 functionCall 整帧下发。
package translate

import (
	"encoding/json"
	"strings"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// geminiToolBuf 是一个待冲刷的 functionCall 缓冲。
type geminiToolBuf struct {
	id, name string
	args     strings.Builder
}

// GeminiStreamEncoder 是有状态流编码器（非并发安全）。
type GeminiStreamEncoder struct {
	model    string
	finished bool
	tools    []*geminiToolBuf // 到达序
	toolIdx  map[int]*geminiToolBuf
	lastTool *geminiToolBuf // 无 index 流的最近调用
}

// NewGeminiStreamEncoder 装配编码器。
func NewGeminiStreamEncoder() *GeminiStreamEncoder { return &GeminiStreamEncoder{} }

// Feed 消费一个归一化 chunk，产出该批 SSE 帧（可为空）。
func (e *GeminiStreamEncoder) Feed(c *schema.Chunk) [][]byte {
	var frames [][]byte
	if e.model == "" {
		e.model = c.Model
	}
	for _, ch := range c.Choices {
		for _, tc := range ch.Delta.ToolCalls {
			e.toolBufFor(tc).args.WriteString(tc.Function.Arguments)
		}
		if ch.FinishReason != "" {
			frames = append(frames, e.flushTools()...)
		}
		var cand GeminiCandidate
		cand.Content.Role = "model"
		if text := ch.Delta.Content.TextOf(); text != "" {
			cand.Content.Parts = append(cand.Content.Parts, GeminiPart{Text: text})
		}
		if ch.FinishReason != "" {
			cand.FinishReason = MapFinishToGemini(ch.FinishReason)
			e.finished = true
		}
		if len(cand.Content.Parts) == 0 && cand.FinishReason == "" {
			continue
		}
		frames = append(frames, e.frame(&GeminiResponse{
			Candidates:   []GeminiCandidate{cand},
			ModelVersion: e.model,
		}))
	}
	if c.Usage != nil {
		frames = append(frames, e.frame(&GeminiResponse{
			UsageMetadata: &GeminiUsage{
				PromptTokenCount:     c.Usage.PromptTokens,
				CandidatesTokenCount: c.Usage.CompletionTokens,
				TotalTokenCount:      c.Usage.TotalTokens,
			},
			ModelVersion: e.model,
		}))
	}
	return frames
}

// toolBufFor 定位/新建该 tool_call 的参数缓冲：有 index 按 index，
// 无 index 时带 id/name 视作新调用、纯参数片段续最近调用。
func (e *GeminiStreamEncoder) toolBufFor(tc schema.ToolCall) *geminiToolBuf {
	if tc.Index != nil {
		if b, ok := e.toolIdx[*tc.Index]; ok {
			return b
		}
	}
	if tc.Index == nil && tc.ID == "" && tc.Function.Name == "" && e.lastTool != nil {
		return e.lastTool
	}
	b := &geminiToolBuf{id: tc.ID, name: tc.Function.Name}
	e.tools = append(e.tools, b)
	if tc.Index != nil {
		if e.toolIdx == nil {
			e.toolIdx = map[int]*geminiToolBuf{}
		}
		e.toolIdx[*tc.Index] = b
	}
	e.lastTool = b
	return b
}

// flushTools 把缓冲的 functionCall 拼成完整 parts 整帧下发。
func (e *GeminiStreamEncoder) flushTools() [][]byte {
	if len(e.tools) == 0 {
		return nil
	}
	parts := make([]GeminiPart, 0, len(e.tools))
	for _, b := range e.tools {
		parts = append(parts, GeminiPart{FunctionCall: &GeminiFunctionCall{
			ID: b.id, Name: b.name, Args: geminiArgsOf(b.args.String()),
		}})
	}
	e.tools, e.toolIdx, e.lastTool = nil, nil, nil
	return [][]byte{e.frame(&GeminiResponse{
		Candidates: []GeminiCandidate{{
			Content: GeminiContent{Role: "model", Parts: parts},
		}},
		ModelVersion: e.model,
	})}
}

// Finish 产出收尾帧：先冲刷未下的 functionCall，未见 finish_reason 时
// 补一帧 STOP（优雅收尾）。
func (e *GeminiStreamEncoder) Finish() [][]byte {
	frames := e.flushTools()
	if e.finished {
		return frames
	}
	e.finished = true
	return append(frames, e.frame(&GeminiResponse{
		Candidates: []GeminiCandidate{{
			Content:      GeminiContent{Role: "model"},
			FinishReason: "STOP",
		}},
		ModelVersion: e.model,
	}))
}

// frame 拼一帧 Gemini SSE（Google 线上形以 \r\n 定界）。
func (e *GeminiStreamEncoder) frame(r *GeminiResponse) []byte {
	data, err := json.Marshal(r)
	if err != nil {
		data = []byte(`{"candidates":[]}`) // 受控结构，兜底防断流
	}
	buf := make([]byte, 0, len(data)+16)
	buf = append(buf, "data: "...)
	buf = append(buf, data...)
	return append(buf, "\r\n\r\n"...)
}
