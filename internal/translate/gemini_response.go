// gemini_response.go 是 Gemini 入站的响应侧翻译：IR 聚合响应
// → GeminiResponse wire 形，含 finishReason 双向映射。
package translate

import (
	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// GeminiResponse 是 generateContent 响应体。
type GeminiResponse struct {
	ResponseID    string            `json:"responseId,omitempty"`
	Candidates    []GeminiCandidate `json:"candidates"`
	UsageMetadata *GeminiUsage      `json:"usageMetadata,omitempty"`
	ModelVersion  string            `json:"modelVersion,omitempty"`
}

// GeminiCandidate 是一条候选输出。
type GeminiCandidate struct {
	Content      GeminiContent `json:"content,omitempty"`
	FinishReason string        `json:"finishReason,omitempty"`
	Index        int           `json:"index"`
}

// GeminiUsage 是 Gemini 口径的用量。
type GeminiUsage struct {
	PromptTokenCount     int `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount int `json:"candidatesTokenCount,omitempty"`
	TotalTokenCount      int `json:"totalTokenCount,omitempty"`
}

// ToGeminiGenerateContent 把 IR 聚合响应渲染为 Gemini wire 形。
// 文本 part 拼进 model 角色的 parts；finish_reason 映射见
// MapFinishToGemini；usage 改名 prompt/candidatesTokenCount。
func ToGeminiGenerateContent(resp *schema.Response) *GeminiResponse {
	out := &GeminiResponse{ResponseID: resp.ID, ModelVersion: resp.Model}
	var cand GeminiCandidate
	cand.Content.Role = "model"
	for _, ch := range resp.Choices {
		for _, p := range ch.Message.Content.Parts {
			if p.Type == schema.PartText {
				cand.Content.Parts = append(cand.Content.Parts, GeminiPart{Text: p.Text})
			}
		}
		for _, c := range ch.Message.ToolCalls {
			cand.Content.Parts = append(cand.Content.Parts, GeminiPart{
				FunctionCall: &GeminiFunctionCall{
					ID: c.ID, Name: c.Function.Name,
					Args: geminiArgsOf(c.Function.Arguments),
				},
			})
		}
		if ch.FinishReason != "" {
			cand.FinishReason = MapFinishToGemini(ch.FinishReason)
		}
	}
	if cand.FinishReason == "" {
		cand.FinishReason = "STOP"
	}
	out.Candidates = []GeminiCandidate{cand}
	if resp.Usage != nil {
		out.UsageMetadata = &GeminiUsage{
			PromptTokenCount:     resp.Usage.PromptTokens,
			CandidatesTokenCount: resp.Usage.CompletionTokens,
			TotalTokenCount:      resp.Usage.TotalTokens,
		}
	}
	return out
}

// MapFinishToGemini 把 OpenAI finish_reason 映射为 Gemini finishReason。
func MapFinishToGemini(finish string) string {
	switch finish {
	case schema.FinishLength:
		return "MAX_TOKENS"
	case schema.FinishContentFilt:
		return "SAFETY"
	default: // stop / tool_calls（前不会出现）按正常收尾
		return "STOP"
	}
}

// MapGeminiFinish 把 Gemini finishReason 映射为 OpenAI finish_reason。
// 安全类（SAFETY/RECITATION/LANGUAGE/BLOCKLIST/PROHIBITED_CONTENT/
// SPII/IMAGE_SAFETY）统一归 content_filter。
func MapGeminiFinish(reason string) string {
	switch reason {
	case "MAX_TOKENS":
		return schema.FinishLength
	case "SAFETY", "RECITATION", "LANGUAGE", "BLOCKLIST",
		"PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY":
		return schema.FinishContentFilt
	case "MALFORMED_FUNCTION_CALL":
		return schema.FinishToolCalls
	default: // STOP / OTHER / 未知
		return schema.FinishStop
	}
}
