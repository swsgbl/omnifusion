// stream_fold_test.go 覆盖体形嗅探：流请求收到整份 JSON 文档
// （Content-Type 撒谎或网关不支持流式）不再误判为断流——折叠为
// 单帧 chunk 序列，message 与 reasoning 完整、usage 附末帧。
package openai_compat

import (
	"context"
	"io"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
)

func TestParseStreamFoldsJSONBody(t *testing.T) {
	a := newTestAdapter(t)
	doc := `{"id":"chatcmpl-1","object":"chat.completion","created":1756000000,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"答案","reasoning_content":"思考"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":6,"total_tokens":11}}`
	resp := sseResponse(t, 200, doc)
	resp.Header.Set("Content-Type", "application/json")

	stream, err := a.ParseStream(context.Background(), &provider.ProviderCall{Model: "m"}, resp)
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	defer stream.Close()

	chunk, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(chunk.Choices) != 1 {
		t.Fatalf("choices = %d", len(chunk.Choices))
	}
	if got := chunk.Choices[0].Delta.Content.TextOf(); got != "答案" {
		t.Errorf("folded content = %q", got)
	}
	if chunk.Choices[0].Delta.Reasoning != "思考" || chunk.Choices[0].Delta.ReasoningKey != "reasoning_content" {
		t.Errorf("folded reasoning = (%q,%q)", chunk.Choices[0].Delta.Reasoning, chunk.Choices[0].Delta.ReasoningKey)
	}
	if chunk.Choices[0].FinishReason != "stop" {
		t.Errorf("finish = %q", chunk.Choices[0].FinishReason)
	}
	if chunk.Usage == nil || chunk.Usage.TotalTokens != 11 {
		t.Errorf("usage = %+v", chunk.Usage)
	}
	if _, err := stream.Next(context.Background()); err != io.EOF {
		t.Errorf("second Next = %v, want EOF", err)
	}
}

// TestParseStreamSniffLeadingWhitespace 体形判定跳过前导空白后的
// 第一个字节。
func TestParseStreamSniffLeadingWhitespace(t *testing.T) {
	a := newTestAdapter(t)
	doc := "\r\n  {\"id\":\"x\",\"choices\":[{\"index\":0,\"message\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}"
	stream, err := a.ParseStream(context.Background(), nil, sseResponse(t, 200, doc))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	defer stream.Close()
	chunk, err := stream.Next(context.Background())
	if err != nil || len(chunk.Choices) != 1 || chunk.Choices[0].Delta.Content.TextOf() != "ok" {
		t.Fatalf("fold failed: chunk=%+v err=%v", chunk, err)
	}
}

// TestParseStreamSSEUnaffected 正常 SSE 流（含前导注释行）不走进
// JSON 折叠路径。
func TestParseStreamSSEUnaffected(t *testing.T) {
	a := newTestAdapter(t)
	sse := ": keep-alive\n\ndata: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	stream, err := a.ParseStream(context.Background(), nil, sseResponse(t, 200, sse))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	defer stream.Close()
	chunk, err := stream.Next(context.Background())
	if err != nil || len(chunk.Choices) != 1 || chunk.Choices[0].Delta.Content.TextOf() != "hi" {
		t.Fatalf("sse path broken: chunk=%+v err=%v", chunk, err)
	}
	if _, err := stream.Next(context.Background()); err != io.EOF {
		t.Errorf("Next after DONE = %v, want EOF", err)
	}
}
