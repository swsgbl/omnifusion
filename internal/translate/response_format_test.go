package translate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// mustGeminiWire 是出站对的测试便捷形（起出站对附带 degraded
// 返回值，matrix/tools 测试经此取 wire；Anthropic 侧 fixture 已由
// 各测试内联，helper 不再保留——CI unused 审计）。
func mustGeminiWire(t *testing.T, ir *schema.UnifiedRequest) *GeminiRequest {
	t.Helper()
	wire, degraded := ToGeminiUpstreamRequest(ir)
	if len(degraded) != 0 {
		t.Fatalf("unexpected degraded %v in fixture", degraded)
	}
	return wire
}

func TestParseResponseFormat(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantMime string
		wantKey  string // 期望 schema JSON 里出现/不出现的键
		wantIn   bool
		ok       bool
	}{
		{"json_object", `{"type":"json_object"}`, "application/json", "", false, true},
		{"json_schema strips unsupported", `{"type":"json_schema","json_schema":{"schema":{"type":"object","$schema":"x","properties":{"a":{"type":"string","additionalProperties":false}}}}}`,
			"application/json", "additionalProperties", false, true},
		{"unknown type", `{"type":"yaml"}`, "", "", false, false},
		{"json_schema without schema", `{"type":"json_schema","json_schema":{"name":"n"}}`, "", "", false, false},
		{"garbage", `{not json`, "", "", false, false},
	}
	for _, tc := range cases {
		spec, ok := parseResponseFormat(json.RawMessage(tc.raw))
		if ok != tc.ok {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if spec.mime != tc.wantMime {
			t.Errorf("%s: mime = %q, want %q", tc.name, spec.mime, tc.wantMime)
		}
		if tc.wantKey != "" {
			if got := string(spec.schema); strings.Contains(got, tc.wantKey) != tc.wantIn {
				t.Errorf("%s: schema %s contains %q = %v, want %v",
					tc.name, got, tc.wantKey, !tc.wantIn, tc.wantIn)
			}
		}
	}
}

func TestResponseFormatFromGemini(t *testing.T) {
	if rf := responseFormatFromGemini("text/plain", nil); rf != nil {
		t.Errorf("text/plain → %s, want nil", rf)
	}
	b, err := json.Marshal(responseFormatFromGemini("application/json", nil))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `{"type":"json_object"}` {
		t.Errorf("mime-only → %s", b)
	}
	b, err = json.Marshal(responseFormatFromGemini("application/json", json.RawMessage(`{"type":"object"}`)))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Type string `json:"type"`
		JS   struct {
			Schema map[string]any `json:"schema"`
		} `json:"json_schema"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != "json_schema" || got.JS.Schema["type"] != "object" {
		t.Errorf("mime+schema → %s", b)
	}
}

func rfRequest(t *testing.T, raw string) *schema.UnifiedRequest {
	t.Helper()
	return &schema.UnifiedRequest{
		Model:          "m",
		Messages:       []schema.Message{{Role: "user", Content: schema.NewTextContent("hi")}},
		ResponseFormat: json.RawMessage(raw),
	}
}

func TestAnthropicUpstreamResponseFormatDegraded(t *testing.T) {
	wire, degraded := ToAnthropicUpstreamRequest(rfRequest(t, `{"type":"json_object"}`))
	if len(degraded) != 1 || degraded[0] != "response_format" {
		t.Fatalf("degraded = %v, want [response_format]", degraded)
	}
	body, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), "response_format") {
		t.Errorf("wire leaked response_format: %s", body)
	}

	wire, degraded = ToAnthropicUpstreamRequest(&schema.UnifiedRequest{Model: "m"})
	if wire == nil || len(degraded) != 0 {
		t.Errorf("no response_format: degraded = %v, want empty", degraded)
	}
}

func TestGeminiUpstreamResponseFormat(t *testing.T) {
	wire, degraded := ToGeminiUpstreamRequest(rfRequest(t, `{"type":"json_object"}`))
	if len(degraded) != 0 || wire.GenerationConfig == nil {
		t.Fatalf("json_object: degraded = %v, gc = %+v", degraded, wire.GenerationConfig)
	}
	if wire.GenerationConfig.ResponseMimeType != "application/json" || wire.GenerationConfig.ResponseSchema != nil {
		t.Errorf("json_object: mime = %q, schema = %s",
			wire.GenerationConfig.ResponseMimeType, wire.GenerationConfig.ResponseSchema)
	}

	wire, degraded = ToGeminiUpstreamRequest(rfRequest(t,
		`{"type":"json_schema","json_schema":{"schema":{"type":"object","$schema":"drop-me"}}}`))
	if len(degraded) != 0 {
		t.Fatalf("json_schema: degraded = %v", degraded)
	}
	if s := string(wire.GenerationConfig.ResponseSchema); strings.Contains(s, "drop-me") {
		t.Errorf("schema not stripped: %s", s)
	}

	wire, degraded = ToGeminiUpstreamRequest(rfRequest(t, `{"type":"yaml"}`))
	if len(degraded) != 1 || degraded[0] != "response_format" {
		t.Fatalf("unknown type: degraded = %v", degraded)
	}
	if gc := wire.GenerationConfig; gc != nil && gc.ResponseMimeType != "" {
		t.Errorf("unknown type leaked mime %q", gc.ResponseMimeType)
	}
}

func TestFromGeminiResponseFormat(t *testing.T) {
	in := &GeminiRequest{
		Contents: []GeminiContent{{Role: "user", Parts: []GeminiPart{{Text: "hi"}}}},
		GenerationConfig: &GeminiGeneration{
			ResponseMimeType: "application/json",
			ResponseSchema:   json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`),
		},
	}
	req, _ := FromGeminiGenerateContent("gemini-x", in, false)
	var got struct {
		Type string `json:"type"`
		JS   struct {
			Schema map[string]any `json:"schema"`
		} `json:"json_schema"`
	}
	if err := json.Unmarshal(req.ResponseFormat, &got); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, req.ResponseFormat)
	}
	if got.Type != "json_schema" || got.JS.Schema["type"] != "object" {
		t.Errorf("IR response_format = %s", req.ResponseFormat)
	}

	in.GenerationConfig.ResponseSchema = nil
	req, _ = FromGeminiGenerateContent("gemini-x", in, false)
	if string(req.ResponseFormat) != `{"type":"json_object"}` {
		t.Errorf("mime-only IR response_format = %s", req.ResponseFormat)
	}
}
