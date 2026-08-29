// response_test.go 覆盖响应与流式 chunk 的解析。
package schema

import (
	"encoding/json"
	"testing"
)

func TestResponseParse(t *testing.T) {
	var resp Response
	if err := json.Unmarshal([]byte(sampleResponse), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ID != "chatcmpl-123" || resp.Object != "chat.completion" {
		t.Fatalf("header mismatch: %+v", resp)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices = %d", len(resp.Choices))
	}
	ch := resp.Choices[0]
	if ch.FinishReason != FinishToolCalls || ch.Message.Content.TextOf() != "Hi!" {
		t.Errorf("choice mismatch: %+v", ch)
	}
	if len(ch.Message.ToolCalls) != 1 || ch.Message.ToolCalls[0].ID != "call_9" {
		t.Errorf("tool calls mismatch: %+v", ch.Message.ToolCalls)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 21 {
		t.Errorf("usage mismatch: %+v", resp.Usage)
	}
}

const sampleChunk = `{
  "id": "chatcmpl-123",
  "object": "chat.completion.chunk",
  "created": 1756300000,
  "model": "groq/llama-3.3-70b",
  "choices": [{
    "index": 0,
    "delta": {"tool_calls": [{"index": 0, "id": "call_9", "type": "function", "function": {"name": "f", "arguments": "{\"a\":"}}]},
    "finish_reason": null
  }]
}`

func TestChunkParse(t *testing.T) {
	var ck Chunk
	if err := json.Unmarshal([]byte(sampleChunk), &ck); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ck.Object != "chat.completion.chunk" || len(ck.Choices) != 1 {
		t.Fatalf("chunk mismatch: %+v", ck)
	}
	delta := ck.Choices[0].Delta
	if len(delta.ToolCalls) != 1 {
		t.Fatalf("delta tool_calls = %d", len(delta.ToolCalls))
	}
	tc := delta.ToolCalls[0]
	if tc.Index == nil || *tc.Index != 0 || tc.Function.Arguments != `{"a":` {
		t.Errorf("delta tool call mismatch: %+v", tc)
	}
	if ck.Choices[0].FinishReason != "" {
		t.Errorf("finish_reason should decode null as empty, got %q", ck.Choices[0].FinishReason)
	}
}
