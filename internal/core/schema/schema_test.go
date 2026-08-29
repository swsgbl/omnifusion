package schema

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

const sampleRequest = `{
  "model": "openrouter/auto",
  "messages": [
    {"role": "system", "content": "You are terse."},
    {"role": "user", "content": [
      {"type": "text", "text": "What is in this image?"},
      {"type": "image_url", "image_url": {"url": "https://example.com/a.png", "detail": "low"}}
    ]},
    {"role": "assistant", "content": null, "tool_calls": [
      {"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"SF\"}"}}
    ]},
    {"role": "tool", "tool_call_id": "call_1", "content": "{\"temp\":15}"}
  ],
  "stream": true,
  "temperature": 0.2,
  "max_tokens": 512,
  "tools": [
    {"type": "function", "function": {
      "name": "get_weather",
      "description": "Get weather",
      "parameters": {"type": "object", "properties": {"city": {"type": "string"}}},
      "strict": true
    }}
  ],
  "tool_choice": {"type": "function", "function": {"name": "get_weather"}},
  "stream_options": {"include_usage": true},
  "frequency_penalty": 0.1,
  "x_custom_flag": {"deep": [1, 2]}
}`

func TestUnifiedRequestParse(t *testing.T) {
	var req UnifiedRequest
	if err := json.Unmarshal([]byte(sampleRequest), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if req.Model != "openrouter/auto" || !req.Stream {
		t.Fatalf("model/stream mismatch: %+v", req)
	}
	if len(req.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(req.Messages))
	}
	if got := req.Messages[0].Content.TextOf(); got != "You are terse." {
		t.Errorf("system text = %q", got)
	}

	// 多模态：文本 + image_url
	user := req.Messages[1]
	if len(user.Content.Parts) != 2 {
		t.Fatalf("user parts = %d, want 2", len(user.Content.Parts))
	}
	img := user.Content.Parts[1]
	if img.Type != PartImageURL || img.ImageURL == nil ||
		img.ImageURL.URL != "https://example.com/a.png" || img.ImageURL.Detail != "low" {
		t.Errorf("image part mismatch: %+v", img)
	}

	// assistant 工具调用消息：content 为 null
	asst := req.Messages[2]
	if len(asst.Content.Parts) != 0 {
		t.Errorf("assistant content should be empty, got %+v", asst.Content)
	}
	if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].ID != "call_1" ||
		asst.ToolCalls[0].Function.Name != "get_weather" ||
		asst.ToolCalls[0].Function.Arguments != `{"city":"SF"}` {
		t.Errorf("tool call mismatch: %+v", asst.ToolCalls)
	}

	// tool 结果消息
	tool := req.Messages[3]
	if tool.Role != RoleTool || tool.ToolCallID != "call_1" {
		t.Errorf("tool message mismatch: %+v", tool)
	}

	// 数值指针字段
	if req.Temperature == nil || *req.Temperature != 0.2 {
		t.Errorf("temperature = %v", req.Temperature)
	}
	if req.MaxTokens == nil || *req.MaxTokens != 512 {
		t.Errorf("max_tokens = %v", req.MaxTokens)
	}
	if req.Seed != nil {
		t.Errorf("seed should be nil, got %v", *req.Seed)
	}

	// 工具定义
	if len(req.Tools) != 1 {
		t.Fatalf("tools = %d", len(req.Tools))
	}
	fn := req.Tools[0].Function
	if fn.Name != "get_weather" || fn.Strict == nil || !*fn.Strict {
		t.Errorf("tool function mismatch: %+v", fn)
	}
	if !json.Valid(fn.Parameters) {
		t.Errorf("parameters not preserved as raw JSON")
	}

	// tool_choice 对象形态
	if req.ToolChoice == nil || req.ToolChoice.Mode != ToolChoiceFunction ||
		req.ToolChoice.Function != "get_weather" {
		t.Errorf("tool_choice mismatch: %+v", req.ToolChoice)
	}

	// 透传字段按序捕获
	keys := req.Extra.Keys()
	if !reflect.DeepEqual(keys, []string{"frequency_penalty", "x_custom_flag"}) {
		t.Errorf("extra keys = %v", keys)
	}
	if v, ok := req.Extra.Get("frequency_penalty"); !ok || string(v) != "0.1" {
		t.Errorf("frequency_penalty = %s", v)
	}
}

func TestUnifiedRequestRoundTrip(t *testing.T) {
	var req UnifiedRequest
	if err := json.Unmarshal([]byte(sampleRequest), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var again UnifiedRequest
	if err := json.Unmarshal(out, &again); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	// encoding/json 对 RawMessage 做空白压缩，语义等价即视为往返成功。
	normalizeRaw(&req)
	normalizeRaw(&again)
	if !reflect.DeepEqual(req, again) {
		t.Errorf("round trip mismatch:\n first: %+v\nagain: %+v", req, again)
	}
}

// normalizeRaw 压缩请求中所有原样 JSON 字段的空白，便于字节级比较。
func normalizeRaw(r *UnifiedRequest) {
	compact := func(m *json.RawMessage) {
		if len(*m) == 0 {
			return
		}
		var buf bytes.Buffer
		if json.Compact(&buf, *m) == nil {
			*m = buf.Bytes()
		}
	}
	for i := range r.Tools {
		compact(&r.Tools[i].Function.Parameters)
	}
	compact(&r.StreamOpts)
	compact(&r.ResponseFormat)
	for _, k := range r.Extra.Keys() {
		if v, ok := r.Extra.Get(k); ok {
			compact(&v)
			r.Extra.Set(k, v)
		}
	}
}

func TestToolChoiceStringForm(t *testing.T) {
	for _, mode := range []string{"auto", "none", "required"} {
		var tc ToolChoice
		if err := json.Unmarshal([]byte(`"`+mode+`"`), &tc); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if tc.Mode != mode {
			t.Errorf("mode = %q, want %q", tc.Mode, mode)
		}
		out, err := json.Marshal(tc)
		if err != nil || string(out) != `"`+mode+`"` {
			t.Errorf("marshal %s -> %s (%v)", mode, out, err)
		}
	}
}
