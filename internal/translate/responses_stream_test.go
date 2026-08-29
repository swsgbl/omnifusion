package translate

import (
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// feedTextChunk 造一个文本增量 chunk。
func feedTextChunk(id, model string, text string, idx int) *schema.Chunk {
	c := schema.NewChunk(id, model, 7)
	c.Choices = []schema.ChunkChoice{{Index: idx, Delta: schema.Message{
		Role: schema.RoleAssistant, Content: schema.NewTextContent(text),
	}}}
	return c
}

func TestResponsesStreamEventSequence(t *testing.T) {
	enc := NewResponsesStreamEncoder()
	var all []string
	for _, f := range enc.Feed(feedTextChunk("c1", "model-a", "Hel", 0)) {
		all = append(all, string(f))
	}
	for _, f := range enc.Feed(feedTextChunk("c1", "model-a", "lo", 0)) {
		all = append(all, string(f))
	}
	tail := schema.NewChunk("c1", "model-a", 7)
	tail.Usage = &schema.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}
	tail.Choices = []schema.ChunkChoice{{Index: 0, Delta: schema.Message{}, FinishReason: "stop"}}
	for _, f := range enc.Feed(tail) {
		all = append(all, string(f))
	}
	for _, f := range enc.Finish() {
		all = append(all, string(f))
	}

	joined := strings.Join(all, "")
	wantOrder := []string{
		"event: response.created\n",
		"event: response.output_item.added\n",
		"event: response.content_part.added\n",
		"event: response.output_text.delta\n",
		"event: response.output_text.delta\n",
		"event: response.output_text.done\n",
		"event: response.content_part.done\n",
		"event: response.output_item.done\n",
		"event: response.completed\n",
	}
	pos := 0
	for _, w := range wantOrder {
		i := strings.Index(joined[pos:], w)
		if i < 0 {
			t.Fatalf("event %q missing or out of order in:\n%s", w, joined)
		}
		pos += i + len(w)
	}
	if !strings.Contains(joined, `"delta":"Hel"`) || !strings.Contains(joined, `"delta":"lo"`) {
		t.Fatalf("text deltas missing:\n%s", joined)
	}
	if !strings.Contains(joined, `"text":"Hello"`) {
		t.Fatalf("output_text.done must carry full text:\n%s", joined)
	}
	if !strings.Contains(joined, `"input_tokens":3`) || !strings.Contains(joined, `"output_tokens":2`) {
		t.Fatalf("completed usage missing:\n%s", joined)
	}
}

func TestResponsesStreamToolCallsAtFinish(t *testing.T) {
	enc := NewResponsesStreamEncoder()
	c := schema.NewChunk("c2", "model-b", 9)
	i0 := 0
	c.Choices = []schema.ChunkChoice{{Index: 0, Delta: schema.Message{
		ToolCalls: []schema.ToolCall{{
			ID: "call_1", Type: "function", Index: &i0,
			Function: schema.ToolCallFunction{Name: "sh", Arguments: `{"c`},
		}},
	}}}
	enc.Feed(c)
	c2 := schema.NewChunk("c2", "model-b", 9)
	c2.Choices = []schema.ChunkChoice{{
		Index: 0,
		Delta: schema.Message{ToolCalls: []schema.ToolCall{{
			ID: "call_1", Type: "function", Index: &i0,
			Function: schema.ToolCallFunction{Arguments: `md":"ls"}`},
		}}},
		FinishReason: "tool_calls",
	}}
	enc.Feed(c2)

	var joined strings.Builder
	for _, f := range enc.Finish() {
		joined.Write(f)
	}
	s := joined.String()
	if !strings.Contains(s, `"type":"function_call"`) {
		t.Fatalf("function_call item missing:\n%s", s)
	}
	if !strings.Contains(s, `"arguments":"{\"cmd\":\"ls\"}"`) {
		t.Fatalf("fragment arguments not accumulated:\n%s", s)
	}
	if !strings.Contains(s, "event: response.completed") {
		t.Fatalf("completed missing:\n%s", s)
	}
	if strings.Contains(s, "response.output_item.added") == strings.Contains(s, "response.output_text.delta") {
		// 文本 item 未开：不得出现 output_text.delta；function_call 走 added/done
		t.Fatalf("unexpected text item events:\n%s", s)
	}
}

func TestResponsesStreamEmptyFinish(t *testing.T) { // 空流：完整生命周期兜底
	enc := NewResponsesStreamEncoder()
	var joined strings.Builder
	for _, f := range enc.Finish() {
		joined.Write(f)
	}
	s := joined.String()
	if !strings.Contains(s, "event: response.created") || !strings.Contains(s, "event: response.completed") {
		t.Fatalf("empty stream must still emit created+completed:\n%s", s)
	}
	if strings.Contains(s, "output_text") {
		t.Fatalf("no text expected:\n%s", s)
	}
}
