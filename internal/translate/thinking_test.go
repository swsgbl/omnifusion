// thinking_test.go 覆盖思考内容的三协议渲染：Anthropic thinking 块
// （非流+流）、Gemini thought part（非流+流）——上游 reasoning 经
// 网关中继后各协议客户端以原生形态看到思考。
package translate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

func reasoningResponse() *schema.Response {
	return &schema.Response{
		ID: "c1", Model: "m",
		Choices: []schema.ResponseChoice{{
			Message: schema.Message{
				Content:   schema.NewTextContent("答案"),
				Reasoning: "先想再答",
			},
			FinishReason: schema.FinishStop,
		}},
	}
}

func TestAnthropicThinkingBlock(t *testing.T) {
	out := ToAnthropicMessages(reasoningResponse())
	if len(out.Content) != 2 {
		t.Fatalf("blocks = %+v, want thinking+text", out.Content)
	}
	if out.Content[0].Type != "thinking" || out.Content[0].Thinking != "先想再答" {
		t.Errorf("thinking block = %+v", out.Content[0])
	}
	if out.Content[1].Type != "text" || out.Content[1].Text != "答案" {
		t.Errorf("text block = %+v", out.Content[1])
	}
	// 无思考时保持纯 text 块（回归守卫）。
	plain := ToAnthropicMessages(&schema.Response{Choices: []schema.ResponseChoice{{
		Message: schema.Message{Content: schema.NewTextContent("x")},
	}}})
	if len(plain.Content) != 1 || plain.Content[0].Type != "text" {
		t.Errorf("plain blocks = %+v", plain.Content)
	}
}

func TestAnthropicStreamThinkingEvents(t *testing.T) {
	enc := NewAnthropicStreamEncoder()
	var sb strings.Builder
	write := func(frames [][]byte) {
		for _, f := range frames {
			sb.Write(f)
		}
	}
	write(enc.Feed(&schema.Chunk{ID: "c1", Model: "m", Choices: []schema.ChunkChoice{{
		Delta: schema.Message{Reasoning: "思考中"}}}}))
	write(enc.Feed(&schema.Chunk{Choices: []schema.ChunkChoice{{
		Delta: schema.Message{Reasoning: "……", Content: schema.NewTextContent("答案")}}}}))
	write(enc.Feed(&schema.Chunk{Choices: []schema.ChunkChoice{{
		FinishReason: schema.FinishStop}}}))
	write(enc.Finish())
	out := sb.String()

	want := []string{"message_start", "ping",
		"content_block_start", "content_block_delta", "content_block_delta", // thinking 开块+两增量
		"content_block_stop",  // 思考块随首个文本增量收束
		"content_block_start", "content_block_delta", // text 块
		"content_block_stop", "message_delta", "message_stop"}
	if got := eventNames(out); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v\nwant %v", got, want)
	}
	for _, s := range []string{
		`"type":"thinking"`, `"thinking_delta"`, `"thinking":"思考中"`, `"thinking":"……"`,
	} {
		if !strings.Contains(out, s) {
			t.Errorf("stream missing %s", s)
		}
	}
	// 思考块占 index 0，text 块占 1。
	if !strings.Contains(out, `"index":0`) || !strings.Contains(out, `"index":1`) {
		t.Errorf("block indexes missing: %s", out)
	}
}

func TestGeminiThoughtPart(t *testing.T) {
	out := ToGeminiGenerateContent(reasoningResponse())
	parts := out.Candidates[0].Content.Parts
	if len(parts) != 2 {
		t.Fatalf("parts = %+v, want thought+text", parts)
	}
	if !parts[0].Thought || parts[0].Text != "先想再答" {
		t.Errorf("thought part = %+v", parts[0])
	}
	if parts[1].Thought || parts[1].Text != "答案" {
		t.Errorf("text part = %+v", parts[1])
	}
	// 线上形含 "thought":true。
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"thought":true`) {
		t.Errorf("wire missing thought flag: %s", b)
	}
	// 流式：reasoning 增量 → thought part 帧。
	enc := NewGeminiStreamEncoder()
	frames := enc.Feed(&schema.Chunk{ID: "g1", Model: "m", Choices: []schema.ChunkChoice{{
		Delta: schema.Message{Reasoning: "边想"}}}})
	if len(frames) == 0 || !strings.Contains(string(frames[0]), `"thought":true`) {
		t.Errorf("stream frame missing thought: %v", frames)
	}
}
