// gemini_upstream.go 是矩阵的 Gemini 上游列出站对（M3.2）：UnifiedRequest
// → GeminiRequest（上游 wire，model 在 URL 路径里），GeminiResponse →
// UnifiedResponse。与入站对（gemini.go / gemini_response.go）共用同一套
// wire 类型，构成"Gemini 直通"格的两侧。
package translate

import (
	"encoding/json"
	"strings"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// ToGeminiUpstreamRequest 把 IR 翻译为 Gemini generateContent 请求体：
// system 消息抽到顶层 systemInstruction；assistant 映射为 model 角色；
// 图片 part 仅支持 data-URI（→inlineData）与 http(s) URL（→fileData）
// 两种映射，其余静默丢弃属入站侧职责（降级头已标）；采样参数归入
// generationConfig；工具面互译见 gemini_tools.go（M3.3）；结构化输出
// response_format → responseMimeType/responseSchema（M3.6），无法解析
// 的形进 degraded 清单丢弃。
func ToGeminiUpstreamRequest(req *schema.UnifiedRequest) (*GeminiRequest, []string) {
	names := geminiToolNameIndex(req.Messages)
	out := &GeminiRequest{
		Tools:      geminiToolsToWire(req.Tools),
		ToolConfig: geminiToolConfigToWire(req.ToolChoice),
	}
	for _, m := range req.Messages {
		switch m.Role {
		case schema.RoleSystem:
			out.SystemInstruction = geminiMergeInstruction(out.SystemInstruction, m)
		case schema.RoleTool:
			name := names[m.ToolCallID]
			if name == "" {
				name = m.ToolCallID // 前文无对应调用时退化用 id 当 name
			}
			out.Contents = append(out.Contents, GeminiContent{
				Role: "user",
				Parts: []GeminiPart{{FunctionResponse: &GeminiFunctionResponse{
					Name:     name,
					Response: geminiResponsePayload(m.Content.TextOf()),
				}}},
			})
		default:
			out.Contents = append(out.Contents, GeminiContent{
				Role: geminiRole(m.Role), Parts: geminiParts(m),
			})
		}
	}
	var degraded []string
	out.GenerationConfig, degraded = geminiGeneration(req)
	return out, degraded
}

// geminiRole 把 IR 角色映射为 Gemini 角色（user/model 两态）。
func geminiRole(role string) string {
	if role == schema.RoleAssistant {
		return "model"
	}
	return "user"
}

// geminiMergeInstruction 把 system 消息正文并入顶层 systemInstruction。
func geminiMergeInstruction(dst *GeminiContent, m schema.Message) *GeminiContent {
	if dst == nil {
		dst = &GeminiContent{}
	}
	dst.Parts = append(dst.Parts, geminiParts(m)...)
	return dst
}

// geminiGeneration 归拢采样参数与结构化输出（M3.6）；全空则返回 nil
// （省略 generationConfig）。response_format 无法解析时进 degraded。
func geminiGeneration(req *schema.UnifiedRequest) (*GeminiGeneration, []string) {
	gc := &GeminiGeneration{
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		MaxOutputTok:  req.MaxTokens,
		StopSequences: req.Stop,
	}
	var degraded []string
	if len(req.ResponseFormat) > 0 {
		spec, ok := parseResponseFormat(req.ResponseFormat)
		if ok {
			gc.ResponseMimeType = spec.mime
			gc.ResponseSchema = spec.schema
		} else {
			degraded = append(degraded, "response_format")
		}
	}
	if gc.Temperature == nil && gc.TopP == nil && gc.MaxOutputTok == nil &&
		len(gc.StopSequences) == 0 && gc.ResponseMimeType == "" {
		return nil, degraded
	}
	return gc, degraded
}

// geminiParts 把 IR 消息正文映射为 Gemini parts：文本直接映射；图片
// data-URI 拆为 inlineData、http(s) URL 经 fileData 透传；音频 part 还原
// 为 audio/<format> 的 inlineData；tool_calls 追加 functionCall parts。
func geminiParts(m schema.Message) []GeminiPart {
	var out []GeminiPart
	for _, p := range m.Content.Parts {
		switch p.Type {
		case schema.PartText:
			out = append(out, GeminiPart{Text: p.Text})
		case schema.PartImageURL:
			if gp, ok := geminiImagePart(p.ImageURL); ok {
				out = append(out, gp)
			}
		case schema.PartInputAudio:
			if p.InputAudio != nil && p.InputAudio.Data != "" {
				out = append(out, GeminiPart{InlineData: &GeminiInlineData{
					MimeType: "audio/" + p.InputAudio.Format, Data: p.InputAudio.Data}})
			}
		}
	}
	for _, c := range m.ToolCalls {
		out = append(out, GeminiPart{FunctionCall: &GeminiFunctionCall{
			ID: c.ID, Name: c.Function.Name, Args: geminiArgsOf(c.Function.Arguments),
		}})
	}
	if len(out) == 0 {
		out = append(out, GeminiPart{})
	}
	return out
}

// geminiImagePart 把 IR image_url 映射为 Gemini 图片 part。
func geminiImagePart(iu *schema.ImageURL) (GeminiPart, bool) {
	if iu == nil || iu.URL == "" {
		return GeminiPart{}, false
	}
	if mime, data, ok := splitDataURI(iu.URL); ok {
		return GeminiPart{InlineData: &GeminiInlineData{MimeType: mime, Data: data}}, true
	}
	if strings.HasPrefix(iu.URL, "http") {
		raw, _ := json.Marshal(map[string]string{"fileUri": iu.URL})
		return GeminiPart{Raw: json.RawMessage(`{"fileData":` +
			string(raw) + `}`)}, true
	}
	return GeminiPart{}, false
}

// FromGeminiUpstreamResponse 把上游 Gemini 聚合响应归一为 IR：取
// candidates[0]，文本 part 拼进 assistant 消息，functionCall parts 转
// ToolCalls（Gemini 的 STOP 不区分工具调用，有调用时强制
// finish=tool_calls），usageMetadata 改名回 OpenAI 口径。
func FromGeminiUpstreamResponse(resp *GeminiResponse) *schema.Response {
	out := &schema.Response{
		ID:      resp.ResponseID,
		Object:  "chat.completion",
		Model:   resp.ModelVersion,
		Choices: []schema.ResponseChoice{{}},
	}
	msg := &out.Choices[0].Message
	msg.Role = schema.RoleAssistant
	if len(resp.Candidates) > 0 {
		cand := &resp.Candidates[0]
		for _, p := range cand.Content.Parts {
			switch {
			case p.Text != "":
				msg.Content.Parts = append(msg.Content.Parts,
					schema.Part{Type: schema.PartText, Text: p.Text})
			case p.FunctionCall != nil:
				msg.ToolCalls = append(msg.ToolCalls, ToolCallFromGemini(*p.FunctionCall))
			}
		}
		out.Choices[0].FinishReason = MapGeminiFinish(cand.FinishReason)
		if len(msg.ToolCalls) > 0 && out.Choices[0].FinishReason == schema.FinishStop {
			out.Choices[0].FinishReason = schema.FinishToolCalls
		}
	}
	if u := resp.UsageMetadata; u != nil &&
		(u.PromptTokenCount != 0 || u.CandidatesTokenCount != 0) {
		out.Usage = &schema.Usage{
			PromptTokens:     u.PromptTokenCount,
			CompletionTokens: u.CandidatesTokenCount,
			TotalTokens:      u.TotalTokenCount,
		}
	}
	return out
}
