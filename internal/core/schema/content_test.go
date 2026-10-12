// content_test.go 覆盖消息 content 各形态解析与未知类型保留。
package schema

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestContentForms(t *testing.T) {
	// 字符串形态
	var c1 Content
	if err := json.Unmarshal([]byte(`"hello"`), &c1); err != nil {
		t.Fatal(err)
	}
	if c1.TextOf() != "hello" || len(c1.Parts) != 1 {
		t.Errorf("string content mismatch: %+v", c1)
	}
	// null 形态
	var c2 Content
	if err := json.Unmarshal([]byte(`null`), &c2); err != nil {
		t.Fatal(err)
	}
	if len(c2.Parts) != 0 {
		t.Errorf("null content should be empty: %+v", c2)
	}
	// 编码：单文本 → 字符串；空 → null；混合 → 数组
	if b, _ := json.Marshal(c1); string(b) != `"hello"` {
		t.Errorf("single text marshal = %s", b)
	}
	if b, _ := json.Marshal(c2); string(b) != "null" {
		t.Errorf("empty marshal = %s", b)
	}
	mixed := Content{Parts: []Part{
		{Type: PartText, Text: "a"},
		{Type: PartImageURL, ImageURL: &ImageURL{URL: "u"}},
	}}
	b, err := json.Marshal(mixed)
	if err != nil {
		t.Fatal(err)
	}
	var back Content
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(mixed, back) {
		t.Errorf("mixed round trip: %+v vs %+v", mixed, back)
	}
}

func TestUnknownPartTypePreserved(t *testing.T) {
	raw := `{"type":"reasoning","summary":"s"}`
	var p Part
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if p.Type != "reasoning" || string(p.Raw) != raw {
		t.Errorf("unknown part not preserved: %+v", p)
	}
	out, err := json.Marshal(p)
	if err != nil || string(out) != raw {
		t.Errorf("unknown part marshal = %s (%v)", out, err)
	}
}

func TestInputAudioAndFileParts(t *testing.T) {
	var p Part
	err := json.Unmarshal([]byte(`{"type":"input_audio","input_audio":{"data":"QUJD","format":"wav"}}`), &p)
	if err != nil || p.InputAudio == nil || p.InputAudio.Format != "wav" {
		t.Fatalf("input_audio mismatch: %+v (%v)", p, err)
	}
	var f Part
	err = json.Unmarshal([]byte(`{"type":"file","file":{"file_data":"data:application/pdf;base64,QQ==","filename":"a.pdf"}}`), &f)
	if err != nil || f.File == nil || f.File.Filename != "a.pdf" {
		t.Fatalf("file mismatch: %+v (%v)", f, err)
	}
}

// TestMessageReasoningSpellings 验证思考内容的四拼写捕获、同帧双报
// 去重（第一个非空拼写胜出）与按上游拼写回写。
func TestMessageReasoningSpellings(t *testing.T) {
	cases := []struct {
		name, raw, wantKey, wantText string
	}{
		{"openrouter", `{"role":"assistant","reasoning":"think"}`, "reasoning", "think"},
		{"deepseek", `{"role":"assistant","reasoning_content":"深思"}`, "reasoning_content", "深思"},
		{"relay-text", `{"role":"assistant","reasoning_text":"t3"}`, "reasoning_text", "t3"},
		{"claude-chat", `{"role":"assistant","thinking":"t4"}`, "thinking", "t4"},
		{"dup-first-wins", `{"reasoning_content":"dup","reasoning":"dup"}`, "reasoning", "dup"},
		{"empty-skipped", `{"reasoning":"","reasoning_content":"real"}`, "reasoning_content", "real"},
		{"none", `{"role":"user","content":"hi"}`, "", ""},
	}
	for _, tc := range cases {
		var m Message
		if err := json.Unmarshal([]byte(tc.raw), &m); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if m.Reasoning != tc.wantText || m.ReasoningKey != tc.wantKey {
			t.Errorf("%s: got (%q,%q), want (%q,%q)", tc.name,
				m.Reasoning, m.ReasoningKey, tc.wantText, tc.wantKey)
			continue
		}
		if tc.wantKey == "" {
			continue
		}
		out, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("%s marshal: %v", tc.name, err)
		}
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(out, &probe); err != nil {
			t.Fatalf("%s reparse: %v", tc.name, err)
		}
		raw, ok := probe[tc.wantKey]
		if !ok {
			t.Errorf("%s: marshal dropped spelling %q: %s", tc.name, tc.wantKey, out)
			continue
		}
		var s string
		_ = json.Unmarshal(raw, &s)
		if s != tc.wantText {
			t.Errorf("%s: round trip text = %q, want %q", tc.name, s, tc.wantText)
		}
		for _, other := range reasoningSpellings {
			if other != tc.wantKey {
				if _, dup := probe[other]; dup {
					t.Errorf("%s: marshal emitted both %q and %q", tc.name, tc.wantKey, other)
				}
			}
		}
	}
	// 无思考内容时编码与默认形态逐字节一致（回归守卫）。
	plain, err := json.Marshal(Message{Role: "assistant", Content: NewTextContent("ok")})
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != `{"role":"assistant","content":"ok"}` {
		t.Errorf("plain marshal = %s", plain)
	}
}

// TestChunkDeltaReasoningRoundTrip 流式 delta 是 Message：经网关解析再
// 回写后，思考增量以原拼写继续可见。
func TestChunkDeltaReasoningRoundTrip(t *testing.T) {
	raw := `{"id":"c1","choices":[{"index":0,"delta":{"reasoning_content":"正在思考"},"finish_reason":null}]}`
	var c Chunk
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Choices) != 1 || c.Choices[0].Delta.Reasoning != "正在思考" {
		t.Fatalf("delta reasoning not captured: %+v", c.Choices)
	}
	out, err := json.Marshal(&c)
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Choices []struct {
			Delta map[string]json.RawMessage `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatal(err)
	}
	if _, ok := probe.Choices[0].Delta["reasoning_content"]; !ok {
		t.Errorf("delta reasoning_content not re-emitted: %s", out)
	}
}

const sampleResponse = `{
  "id": "chatcmpl-123",
  "object": "chat.completion",
  "created": 1756300000,
  "model": "groq/llama-3.3-70b",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "Hi!", "tool_calls": [
      {"id": "call_9", "type": "function", "function": {"name": "f", "arguments": "{}"}}
    ]},
    "finish_reason": "tool_calls",
    "logprobs": null
  }],
  "usage": {"prompt_tokens": 9, "completion_tokens": 12, "total_tokens": 21},
  "system_fingerprint": "fp_abc"
}`
