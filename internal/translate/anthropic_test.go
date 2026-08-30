package translate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

func TestFromAnthropicMessages(t *testing.T) {
	raw := `{
		"model":"claude-x","max_tokens":1024,
		"system":"be brief",
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":[{"type":"text","text":"hello"}]},
			{"role":"user","content":"bye"}
		],
		"temperature":0.5,"top_p":0.9,"top_k":40,
		"stop_sequences":["\n\n"],"stream":true
	}`
	var in AnthropicRequest
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	req, degraded := FromAnthropicMessages(&in)

	want := []struct{ role, text string }{
		{"system", "be brief"}, {"user", "hi"}, {"assistant", "hello"}, {"user", "bye"},
	}
	if len(req.Messages) != len(want) {
		t.Fatalf("messages = %d, want %d", len(req.Messages), len(want))
	}
	for i, w := range want {
		m := req.Messages[i]
		if m.Role != w.role || m.Content.TextOf() != w.text {
			t.Fatalf("msg[%d] = %s/%q, want %s/%q", i, m.Role, m.Content.TextOf(), w.role, w.text)
		}
	}
	if req.Model != "claude-x" || !req.Stream {
		t.Fatalf("model/stream = %s/%v", req.Model, req.Stream)
	}
	if req.MaxTokens == nil || *req.MaxTokens != 1024 {
		t.Fatalf("max_tokens = %v, want 1024", req.MaxTokens)
	}
	if req.Temperature == nil || *req.Temperature != 0.5 {
		t.Fatalf("temperature = %v, want 0.5", req.Temperature)
	}
	if len(req.Stop) != 1 || req.Stop[0] != "\n\n" {
		t.Fatalf("stop = %v, want [\\n\\n]", req.Stop)
	}
	if len(degraded) != 1 || degraded[0] != "top_k" {
		t.Fatalf("degraded = %v, want [top_k]", degraded)
	}
}

func TestFromAnthropicMessagesDegraded(t *testing.T) {
	// 随后 tools/tool_choice 已互译，只剩 metadata 进降级清单。
	raw := `{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"x"}],` +
		`"tools":[{"name":"f"}],"tool_choice":{"type":"auto"},"metadata":{"user_id":"u"}}`
	var in AnthropicRequest
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	req, degraded := FromAnthropicMessages(&in)
	want := []string{"metadata"}
	if strings.Join(degraded, ",") != strings.Join(want, ",") {
		t.Fatalf("degraded = %v, want %v", degraded, want)
	}
	if len(req.Tools) != 1 || req.Tools[0].Function.Name != "f" {
		t.Fatalf("tools = %+v, want [f]", req.Tools)
	}
	if req.ToolChoice == nil || req.ToolChoice.Mode != schema.ToolChoiceAuto {
		t.Fatalf("tool_choice = %+v, want auto", req.ToolChoice)
	}
}

func TestToAnthropicMessages(t *testing.T) {
	resp := &schema.Response{ID: "chatcmpl-1", Model: "m", Choices: []schema.ResponseChoice{{
		Index: 0,
		Message: schema.Message{Role: "assistant", Content: schema.Content{Parts: []schema.Part{
			{Type: schema.PartText, Text: "a"}, {Type: schema.PartText, Text: "b"},
		}}},
		FinishReason: schema.FinishLength,
	}}, Usage: &schema.Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8}}

	out := ToAnthropicMessages(resp)
	if out.ID != "msg_chatcmpl-1" {
		t.Fatalf("id = %q, want msg_chatcmpl-1", out.ID)
	}
	if out.Type != "message" || out.Role != "assistant" {
		t.Fatalf("type/role = %s/%s", out.Type, out.Role)
	}
	if len(out.Content) != 2 || out.Content[0].Text != "a" || out.Content[1].Text != "b" {
		t.Fatalf("content = %+v, want two text blocks", out.Content)
	}
	if out.StopReason != "max_tokens" {
		t.Fatalf("stop_reason = %q, want max_tokens", out.StopReason)
	}
	if out.Usage.InputTokens != 3 || out.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v, want 3/5", out.Usage)
	}

	empty := ToAnthropicMessages(&schema.Response{ID: "msg_1", Model: "m"})
	if empty.Content == nil || empty.StopReason != "end_turn" {
		t.Fatalf("empty fallback = %+v", empty)
	}
}

func TestMapStopReason(t *testing.T) {
	cases := map[string]string{
		schema.FinishStop:        "end_turn",
		schema.FinishLength:      "max_tokens",
		schema.FinishToolCalls:   "tool_use",
		schema.FinishContentFilt: "refusal",
		"weird":                  "end_turn",
		"":                       "end_turn",
	}
	for in, want := range cases {
		if got := MapStopReason(in); got != want {
			t.Errorf("MapStopReason(%q) = %q, want %q", in, got, want)
		}
	}
}

// eventNames 按出现顺序抽出 "event: X" 行。
func eventNames(s string) []string {
	var names []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "event: ") {
			names = append(names, strings.TrimPrefix(line, "event: "))
		}
	}
	return names
}

func TestAnthropicStreamEncoderSequence(t *testing.T) {
	enc := NewAnthropicStreamEncoder()
	var sb strings.Builder
	write := func(frames [][]byte) {
		for _, f := range frames {
			sb.Write(f)
		}
	}
	write(enc.Feed(&schema.Chunk{ID: "c1", Model: "m", Choices: []schema.ChunkChoice{{
		Delta: schema.Message{Content: schema.NewTextContent("Hel")}}}}))
	write(enc.Feed(&schema.Chunk{Choices: []schema.ChunkChoice{{
		Delta:        schema.Message{Content: schema.NewTextContent("lo")},
		FinishReason: schema.FinishStop}}}))
	write(enc.Finish())
	out := sb.String()

	want := []string{"message_start", "content_block_start", "ping",
		"content_block_delta", "content_block_delta",
		"content_block_stop", "message_delta", "message_stop"}
	if got := eventNames(out); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for _, s := range []string{`"model":"m"`, `"text":"Hel"`, `"text":"lo"`,
		`"stop_reason":"end_turn"`, `"output_tokens":5`} { // 无 usage → 字符估算
		if !strings.Contains(out, s) {
			t.Errorf("stream missing %s", s)
		}
	}
}

func TestAnthropicStreamEncoderUsageWins(t *testing.T) {
	enc := NewAnthropicStreamEncoder()
	enc.Feed(&schema.Chunk{ID: "c1", Model: "m", Choices: []schema.ChunkChoice{{
		Delta: schema.Message{Content: schema.NewTextContent("abcdef")}}}})
	enc.Feed(&schema.Chunk{Usage: &schema.Usage{PromptTokens: 2, CompletionTokens: 7},
		Choices: []schema.ChunkChoice{{FinishReason: schema.FinishLength}}})
	var sb strings.Builder
	for _, f := range enc.Finish() {
		sb.Write(f)
	}
	out := sb.String()
	if !strings.Contains(out, `"output_tokens":7`) { // usage 权威值压过字符估算 6
		t.Errorf("usage should win: %s", out)
	}
	if !strings.Contains(out, `"stop_reason":"max_tokens"`) {
		t.Errorf("finish should map: %s", out)
	}
}
