package anthropic

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
)

// anthropicSSE 是一条完整的 Messages 流事件序列（Google/Anthropic 线上形）。
const anthropicSSE = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-x","usage":{"input_tokens":12,"output_tokens":1}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: ping\n" +
	`data: {"type":"ping"}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":7}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

func TestParseStreamEventSequence(t *testing.T) {
	a, err := New(baseSpec())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	stream, err := a.ParseStream(context.Background(),
		&provider.ProviderCall{Model: "claude-x"}, httpResponse(t, 200, anthropicSSE))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	defer stream.Close()

	ctx := context.Background()
	var texts []string
	var promptTok, completionTok int
	var role, finish, model, id string
	for {
		chunk, err := stream.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		id, model = chunk.ID, chunk.Model
		for _, ch := range chunk.Choices {
			if ch.Delta.Role != "" {
				role = ch.Delta.Role
			}
			if t := ch.Delta.Content.TextOf(); t != "" {
				texts = append(texts, t)
			}
			if ch.FinishReason != "" {
				finish = ch.FinishReason
			}
		}
		if chunk.Usage != nil {
			promptTok += chunk.Usage.PromptTokens
			completionTok = chunk.Usage.CompletionTokens
		}
		if chunk.ProviderName != "mock" {
			t.Errorf("ProviderName = %q", chunk.ProviderName)
		}
	}
	if strings.Join(texts, "") != "Hello" {
		t.Errorf("text = %q, want Hello", strings.Join(texts, ""))
	}
	if role != "assistant" {
		t.Errorf("role = %q, want assistant", role)
	}
	if finish != "length" {
		t.Errorf("finish = %q, want length (max_tokens)", finish)
	}
	if promptTok != 12 || completionTok != 7 {
		t.Errorf("usage = %d/%d, want 12/7", promptTok, completionTok)
	}
	if id != "msg_01" || model != "claude-x" {
		t.Errorf("id/model = %q/%q", id, model)
	}
}

func TestParseStreamMidStreamError(t *testing.T) {
	a, err := New(baseSpec())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_01","usage":{"input_tokens":1}}}` + "\n\n" +
		"event: error\n" +
		`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}` + "\n\n"
	stream, err := a.ParseStream(context.Background(), nil, httpResponse(t, 200, sse))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	defer stream.Close()
	if _, err := stream.Next(context.Background()); err != nil {
		t.Fatalf("message_start should yield a chunk first: %v", err)
	}
	_, err = stream.Next(context.Background())
	if err == nil {
		t.Fatal("error event must surface as error")
	}
	ue, ok := provider.IsUpstream(err)
	if !ok {
		t.Fatalf("err = %v, want UpstreamError", err)
	}
	if ue.Status != 0 {
		t.Errorf("Status = %d, want 0 (no HTTP status mid-stream)", ue.Status)
	}
}

func TestParseStreamEndsWithoutMessageStop(t *testing.T) {
	a, err := New(baseSpec())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_01"}}` + "\n\n"
	stream, err := a.ParseStream(context.Background(), nil, httpResponse(t, 200, sse))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	defer stream.Close()
	if _, err := stream.Next(context.Background()); err != nil {
		t.Fatalf("message_start should yield a chunk: %v", err)
	}
	_, err = stream.Next(context.Background())
	se, ok := provider.AsStreamError(err)
	if !ok {
		t.Fatalf("err = %v, want StreamError", err)
	}
	if se.Reason != provider.StreamEndedWithoutDone {
		t.Errorf("Reason = %q, want ended_without_done", se.Reason)
	}
}

func TestParseStreamNon2xx(t *testing.T) {
	a, err := New(baseSpec())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = a.ParseStream(context.Background(), nil,
		httpResponse(t, 401, `{"type":"error","error":{"type":"authentication_error"}}`))
	ue, ok := provider.IsUpstream(err)
	if !ok {
		t.Fatalf("err = %v, want UpstreamError", err)
	}
	if ue.Status != 401 {
		t.Errorf("Status = %d, want 401", ue.Status)
	}
}

// TestParseStreamToolUse 覆盖 工具流：tool_use 块的 start/delta 序列
// 归一为 OpenAI 口径的 delta.tool_calls（index/id/name 首段 + arguments
// 增量），stop_reason tool_use 反映射 finish=tool_calls。
func TestParseStreamToolUse(t *testing.T) {
	a, err := New(baseSpec())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_02","model":"claude-x"}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	stream, err := a.ParseStream(context.Background(),
		&provider.ProviderCall{Model: "claude-x"}, httpResponse(t, 200, sse))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	defer stream.Close()

	ctx := context.Background()
	var calls []string // "id|name|args|index" per fragment
	var finish string
	for {
		chunk, err := stream.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		for _, ch := range chunk.Choices {
			for _, tc := range ch.Delta.ToolCalls {
				idx := -1
				if tc.Index != nil {
					idx = *tc.Index
				}
				calls = append(calls, tc.ID+"|"+tc.Function.Name+"|"+tc.Function.Arguments+"|"+string(rune('0'+idx)))
			}
			if ch.FinishReason != "" {
				finish = ch.FinishReason
			}
		}
	}
	want := []string{
		"toolu_1|get_weather||0",
		"||{\"city\":|0",
		"||\"Paris\"}|0",
	}
	if len(calls) != len(want) {
		t.Fatalf("tool fragments = %v, want %v", calls, want)
	}
	for i, w := range want {
		if calls[i] != w {
			t.Errorf("fragment[%d] = %q, want %q", i, calls[i], w)
		}
	}
	if finish != "tool_calls" {
		t.Errorf("finish = %q, want tool_calls", finish)
	}
}
