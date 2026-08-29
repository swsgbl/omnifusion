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
