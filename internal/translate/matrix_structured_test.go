package translate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// canonicalStructuredSchema 是 json_schema 的 canonical 样本（Gemini
// 兼容子集；剥离行为在 response_format_test 单测钉）。
const canonicalStructuredSchema = `{
	"type":"object",
	"properties":{
		"name":{"type":"string"},
		"tags":{"type":"array","items":{"type":"string"}}
	},
	"required":["name"]
}`

// structuredIR 是 canonical 结构化输出请求（json_schema 形）。
func structuredIR(t *testing.T) *schema.UnifiedRequest {
	t.Helper()
	return &schema.UnifiedRequest{
		Model: "m",
		Messages: []schema.Message{
			{Role: "user", Content: schema.NewTextContent("give me json")},
		},
		ResponseFormat: json.RawMessage(`{"type":"json_schema","json_schema":{"name":"out","schema":` +
			canonicalStructuredSchema + `}}`),
	}
}

// geminiStructuredInbound 是 Gemini 入站 wire（responseSchema 形），
// 归一后应与 structuredIR 的 ResponseFormat 语义等价。
func geminiStructuredInbound() *GeminiRequest {
	stripped := `{"type":"object","properties":{"name":{"type":"string"},` +
		`"tags":{"type":"array","items":{"type":"string"}}},"required":["name"]}`
	return &GeminiRequest{
		Contents: []GeminiContent{{Role: "user", Parts: []GeminiPart{{Text: "give me json"}}}},
		GenerationConfig: &GeminiGeneration{
			ResponseMimeType: "application/json",
			ResponseSchema:   json.RawMessage(stripped),
		},
	}
}

// TestMatrixStructuredOutput 钉结构化输出的跨协议语义：
// Gemini 入站归一为 IR 的 OpenAI 形 response_format；IR 出站到
// openai_compat（透传）/ gemini（mime+schema，剥离不收键）保语义，
// 到 anthropic（无原生面）显式降级不静默丢。
func TestMatrixStructuredOutput(t *testing.T) {
	ir := structuredIR(t)

	// Gemini 入站 → IR：responseSchema 归一为 json_schema 形，schema
	// 内容与 canonical 剥离后等价（键集合相同）。
	geminiIR, degraded := FromGeminiGenerateContent("m", geminiStructuredInbound(), false)
	if len(degraded) != 0 {
		t.Fatalf("gemini inbound degraded = %v", degraded)
	}
	assertSchemaEquivalent(t, "gemini-inbound", ir.ResponseFormat, geminiIR.ResponseFormat)

	// 出站 openai：IR 即 wire，response_format 原样透传。
	ob, err := json.Marshal(ir)
	if err != nil {
		t.Fatalf("marshal openai wire: %v", err)
	}
	if !strings.Contains(string(ob), `"response_format"`) {
		t.Errorf("openai wire missing response_format: %s", ob)
	}

	// 出站 gemini：mime + schema（$schema/additionalProperties 剥离），
	// 回读 IR 语义保持。
	gwire, degraded := ToGeminiUpstreamRequest(ir)
	if len(degraded) != 0 {
		t.Fatalf("gemini upstream degraded = %v", degraded)
	}
	if gwire.GenerationConfig == nil || gwire.GenerationConfig.ResponseMimeType != "application/json" {
		t.Fatalf("gemini generationConfig = %+v", gwire.GenerationConfig)
	}
	if s := string(gwire.GenerationConfig.ResponseSchema); strings.Contains(s, "$schema") ||
		strings.Contains(s, "additionalProperties") {
		t.Errorf("gemini schema not stripped: %s", s)
	}
	back, _ := FromGeminiGenerateContent("m", gwire, false)
	assertSchemaEquivalent(t, "gemini-roundtrip", ir.ResponseFormat, back.ResponseFormat)

	// 出站 anthropic：wire 不携带 response_format，degraded 显式标记。
	awire, degraded := ToAnthropicUpstreamRequest(ir)
	if len(degraded) != 1 || degraded[0] != "response_format" {
		t.Fatalf("anthropic degraded = %v, want [response_format]", degraded)
	}
	ab, err := json.Marshal(awire)
	if err != nil {
		t.Fatalf("marshal anthropic wire: %v", err)
	}
	if strings.Contains(string(ab), "response_format") {
		t.Errorf("anthropic wire leaked response_format: %s", ab)
	}
}

// assertSchemaEquivalent 断言两个 OpenAI 形 response_format 的
// json_schema.schema 解析后键集合等价（忽略空白与键序）。
func assertSchemaEquivalent(t *testing.T, label string, wantRF, gotRF json.RawMessage) {
	t.Helper()
	var want, got struct {
		JS struct {
			Schema map[string]any `json:"schema"`
		} `json:"json_schema"`
	}
	if err := json.Unmarshal(wantRF, &want); err != nil {
		t.Fatalf("%s: unmarshal want: %v", label, err)
	}
	if err := json.Unmarshal(gotRF, &got); err != nil {
		t.Fatalf("%s: unmarshal got (%s): %v", label, gotRF, err)
	}
	wb, _ := json.Marshal(want.JS.Schema)
	gb, _ := json.Marshal(got.JS.Schema)
	if string(wb) != string(gb) {
		t.Errorf("%s: schema mismatch\nwant %s\ngot  %s", label, wb, gb)
	}
}
