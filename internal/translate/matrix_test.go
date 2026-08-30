// matrix_test.go 钉住三协议互译矩阵的一致性：
// OpenAI / Anthropic / Gemini 三种入站 wire 对同一 canonical 语义归一，
// canonical IR 向三种上游 wire 出站再回读，语义保持。请求侧覆盖
// 3 入站 × 3 上游 = 9 格，响应侧覆盖 3 出站渲染 × 3 回读。
package translate

import (
	"encoding/json"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// canonical 语义常量（fixture 与断言共用）。
const (
	canonModel  = "m-1"
	canonSystem = "You are terse."
	canonUser1  = "Hi"
	canonAsst   = "Hello"
	canonUser2  = "How are you?"
	canonStop   = "END"
	canonTemp   = 0.5
	canonMaxTok = 128
)

var canonRoles = []string{"system", "user", "assistant", "user"}
var canonTexts = []string{canonSystem, canonUser1, canonAsst, canonUser2}

// openaiInboundJSON 是 OpenAI 形入站 wire fixture。
var openaiInboundJSON = `{"model":"` + canonModel + `","messages":[` +
	`{"role":"system","content":"` + canonSystem + `"},` +
	`{"role":"user","content":"` + canonUser1 + `"},` +
	`{"role":"assistant","content":"` + canonAsst + `"},` +
	`{"role":"user","content":"` + canonUser2 + `"}],` +
	`"temperature":` + jsonNum(canonTemp) + `,"max_tokens":128,"stop":["` + canonStop + `"]}`

func jsonNum(v float64) string { b, _ := json.Marshal(v); return string(b) }

func ptrF(v float64) *float64 { return &v }
func ptrI(v int) *int         { return &v }

// anthropicInbound 是 Anthropic 形入站 wire fixture。
func anthropicInbound() *AnthropicRequest {
	return &AnthropicRequest{
		Model:         canonModel,
		MaxTokens:     canonMaxTok,
		Temperature:   ptrF(canonTemp),
		StopSequences: []string{canonStop},
		System:        schema.NewTextContent(canonSystem),
		Messages: []AnthropicMessage{
			{Role: "user", Content: schema.NewTextContent(canonUser1)},
			{Role: "assistant", Content: schema.NewTextContent(canonAsst)},
			{Role: "user", Content: schema.NewTextContent(canonUser2)},
		},
	}
}

// geminiInbound 是 Gemini 形入站 wire fixture。
func geminiInbound() *GeminiRequest {
	return &GeminiRequest{
		SystemInstruction: &GeminiContent{Parts: []GeminiPart{{Text: canonSystem}}},
		Contents: []GeminiContent{
			{Role: "user", Parts: []GeminiPart{{Text: canonUser1}}},
			{Role: "model", Parts: []GeminiPart{{Text: canonAsst}}},
			{Role: "user", Parts: []GeminiPart{{Text: canonUser2}}},
		},
		GenerationConfig: &GeminiGeneration{
			Temperature:   ptrF(canonTemp),
			MaxOutputTok:  ptrI(canonMaxTok),
			StopSequences: []string{canonStop},
		},
	}
}

// assertCanonicalReq 断言 IR 与 canonical 语义等价。
func assertCanonicalReq(t *testing.T, label string, req *schema.UnifiedRequest) {
	t.Helper()
	if req.Model != canonModel {
		t.Errorf("%s: model = %q, want %q", label, req.Model, canonModel)
	}
	if len(req.Messages) != len(canonRoles) {
		t.Fatalf("%s: %d messages, want %d", label, len(req.Messages), len(canonRoles))
	}
	for i, m := range req.Messages {
		if m.Role != canonRoles[i] {
			t.Errorf("%s: msg[%d].role = %q, want %q", label, i, m.Role, canonRoles[i])
		}
		if got := m.Content.TextOf(); got != canonTexts[i] {
			t.Errorf("%s: msg[%d].text = %q, want %q", label, i, got, canonTexts[i])
		}
	}
	if req.Temperature == nil || *req.Temperature != canonTemp {
		t.Errorf("%s: temperature = %v, want %v", label, req.Temperature, canonTemp)
	}
	if req.MaxTokens == nil || *req.MaxTokens != canonMaxTok {
		t.Errorf("%s: max_tokens = %v, want %d", label, req.MaxTokens, canonMaxTok)
	}
	if len(req.Stop) != 1 || req.Stop[0] != canonStop {
		t.Errorf("%s: stop = %v, want [%q]", label, req.Stop, canonStop)
	}
}

// TestMatrixRequests 覆盖 3 入站 × 3 上游 = 9 格：每个入站 wire 归一成
// IR 均等价 canonical；每个 IR 出站到每种上游 wire 再回读仍等价。
func TestMatrixRequests(t *testing.T) {
	var openaiIR schema.UnifiedRequest
	if err := json.Unmarshal([]byte(openaiInboundJSON), &openaiIR); err != nil {
		t.Fatalf("openai fixture: %v", err)
	}
	anthropicIR, degraded := FromAnthropicMessages(anthropicInbound())
	if len(degraded) != 0 {
		t.Errorf("anthropic fixture should not degrade, got %v", degraded)
	}
	geminiIR, degraded := FromGeminiGenerateContent(canonModel, geminiInbound(), false)
	if len(degraded) != 0 {
		t.Errorf("gemini fixture should not degrade, got %v", degraded)
	}
	inbounds := map[string]*schema.UnifiedRequest{
		"openai→IR":    &openaiIR,
		"anthropic→IR": anthropicIR,
		"gemini→IR":    geminiIR,
	}
	for label, ir := range inbounds {
		assertCanonicalReq(t, label, ir)
		for upLabel, roundtrip := range upstreamRoundtrips(ir) {
			assertCanonicalReq(t, label+"→"+upLabel+"→IR", roundtrip)
		}
	}
}

// upstreamRoundtrips 把 IR 渲染为三种上游 wire 再回读为 IR。
func upstreamRoundtrips(ir *schema.UnifiedRequest) map[string]*schema.UnifiedRequest {
	out := map[string]*schema.UnifiedRequest{}
	// openai：IR 即 wire，JSON 往返。
	b, _ := json.Marshal(ir)
	var o schema.UnifiedRequest
	_ = json.Unmarshal(b, &o)
	out["openai"] = &o
	// anthropic：出站对 + 入站对复用同一 wire 类型。
	awire, _ := ToAnthropicUpstreamRequest(ir)
	ab, _ := json.Marshal(awire)
	var ar AnthropicRequest
	if err := json.Unmarshal(ab, &ar); err == nil {
		out["anthropic"], _ = FromAnthropicMessages(&ar)
	}
	// gemini：model 在 URL 路径里，回读时显式传回。
	gwire, _ := ToGeminiUpstreamRequest(ir)
	gb, _ := json.Marshal(gwire)
	var gr GeminiRequest
	if err := json.Unmarshal(gb, &gr); err == nil {
		out["gemini"], _ = FromGeminiGenerateContent(ir.Model, &gr, false)
	}
	return out
}

// TestMatrixResponses 覆盖响应侧 3 出站渲染 × 3 回读。
func TestMatrixResponses(t *testing.T) {
	canonical := &schema.Response{
		ID: canonModel, Object: "chat.completion", Model: canonModel,
		Choices: []schema.ResponseChoice{{
			Message:      schema.Message{Role: schema.RoleAssistant, Content: schema.NewTextContent(canonAsst)},
			FinishReason: schema.FinishStop,
		}},
		Usage: &schema.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	}
	rendered := map[string][]byte{}
	if b, err := json.Marshal(canonical); err == nil {
		rendered["openai"] = b
	}
	if b, err := json.Marshal(ToAnthropicMessages(canonical)); err == nil {
		rendered["anthropic"] = b
	}
	if b, err := json.Marshal(ToGeminiGenerateContent(canonical)); err == nil {
		rendered["gemini"] = b
	}
	// 每种 wire 只被本协议回读：跨协议组合永远经过 IR 中枢（请求
	// 矩阵已覆盖），wire 不直接互解。
	readBack := map[string]func([]byte) (*schema.Response, error){
		"openai": func(b []byte) (*schema.Response, error) {
			var o schema.Response
			err := json.Unmarshal(b, &o)
			return &o, err
		},
		"anthropic": func(b []byte) (*schema.Response, error) {
			var ar AnthropicResponse
			if err := json.Unmarshal(b, &ar); err != nil {
				return nil, err
			}
			return FromAnthropicUpstreamResponse(&ar), nil
		},
		"gemini": func(b []byte) (*schema.Response, error) {
			var gr GeminiResponse
			if err := json.Unmarshal(b, &gr); err != nil {
				return nil, err
			}
			return FromGeminiUpstreamResponse(&gr), nil
		},
	}
	for proto, wire := range rendered {
		resp, err := readBack[proto](wire)
		if err != nil {
			t.Fatalf("%s: read back: %v", proto, err)
		}
		label := proto + "→IR"
		if resp.Model != canonModel {
			t.Errorf("%s: model = %q", label, resp.Model)
		}
		if len(resp.Choices) != 1 ||
			resp.Choices[0].Message.Content.TextOf() != canonAsst {
			t.Fatalf("%s: choices = %+v", label, resp.Choices)
		}
		if resp.Choices[0].FinishReason != schema.FinishStop {
			t.Errorf("%s: finish = %q, want stop", label, resp.Choices[0].FinishReason)
		}
		if resp.Usage == nil || resp.Usage.PromptTokens != 10 ||
			resp.Usage.CompletionTokens != 20 || resp.Usage.TotalTokens != 30 {
			t.Errorf("%s: usage = %+v", label, resp.Usage)
		}
	}
}
