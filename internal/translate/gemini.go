// gemini.go 实现 Gemini generateContent 协议的入站翻译对（
// 矩阵）：GeminiRequest（wire）→ UnifiedRequest（IR），
// UnifiedResponse → GeminiResponse。出站对（IR→上游 Gemini wire）在
// gemini_upstream.go。解析按 proto-JSON 惯例同时接受 camelCase 与
// snake_case 字段名（Google 官方 SDK 发 camelCase，curl 用户常用原名）。
package translate

import (
	"encoding/json"
	"strings"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// GeminiRequest 是 generateContent 请求体（model 在 URL 路径里）。
type GeminiRequest struct {
	Contents          []GeminiContent   `json:"contents,omitempty"`
	SystemInstruction *GeminiContent    `json:"systemInstruction,omitempty"`
	GenerationConfig  *GeminiGeneration `json:"generationConfig,omitempty"`
	Tools             []GeminiTools     `json:"tools,omitempty"`      // 工具互译
	ToolConfig        *GeminiToolConfig `json:"toolConfig,omitempty"` // 
}

// UnmarshalJSON 兼容 proto 原名（snake_case）字段。
func (r *GeminiRequest) UnmarshalJSON(data []byte) error {
	type alias GeminiRequest
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*r = GeminiRequest(a)
	var snake struct {
		SystemInstruction *GeminiContent    `json:"system_instruction"`
		GenerationConfig  *GeminiGeneration `json:"generation_config"`
		ToolConfig        *GeminiToolConfig `json:"tool_config"`
	}
	if err := json.Unmarshal(data, &snake); err != nil {
		return err
	}
	if r.SystemInstruction == nil {
		r.SystemInstruction = snake.SystemInstruction
	}
	if r.GenerationConfig == nil {
		r.GenerationConfig = snake.GenerationConfig
	}
	if r.ToolConfig == nil {
		r.ToolConfig = snake.ToolConfig
	}
	return nil
}

// GeminiContent 是一条消息（role: user/model；system 只出现在
// systemInstruction 里）。
type GeminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []GeminiPart `json:"parts"`
}

// GeminiPart 是内容片段：text、inlineData（内联多模态）或工具面
// functionCall / functionResponse。
type GeminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *GeminiInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *GeminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *GeminiFunctionResponse `json:"functionResponse,omitempty"`
	Raw              json.RawMessage         `json:"-"` // 未知块透传
}

// UnmarshalJSON 按字段分派；未知块保留原始 JSON。
func (p *GeminiPart) UnmarshalJSON(data []byte) error {
	var head struct {
		Text             string                  `json:"text"`
		InlineData       *GeminiInlineData       `json:"inlineData"`
		SnakeData        *GeminiInlineData       `json:"inline_data"`
		FunctionCall     *GeminiFunctionCall     `json:"functionCall"`
		SnakeCall        *GeminiFunctionCall     `json:"function_call"`
		FunctionResponse *GeminiFunctionResponse `json:"functionResponse"`
		SnakeResp        *GeminiFunctionResponse `json:"function_response"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return err
	}
	p.Text, p.InlineData = head.Text, head.InlineData
	if p.InlineData == nil {
		p.InlineData = head.SnakeData
	}
	p.FunctionCall, p.FunctionResponse = head.FunctionCall, head.FunctionResponse
	if p.FunctionCall == nil {
		p.FunctionCall = head.SnakeCall
	}
	if p.FunctionResponse == nil {
		p.FunctionResponse = head.SnakeResp
	}
	if p.Text == "" && p.InlineData == nil &&
		p.FunctionCall == nil && p.FunctionResponse == nil {
		p.Raw = append(json.RawMessage(nil), data...)
	}
	return nil
}

// MarshalJSON 按已建模字段展开。
func (p GeminiPart) MarshalJSON() ([]byte, error) {
	if p.Text != "" {
		return json.Marshal(struct {
			Text string `json:"text"`
		}{p.Text})
	}
	if p.InlineData != nil {
		return json.Marshal(struct {
			InlineData *GeminiInlineData `json:"inlineData"`
		}{p.InlineData})
	}
	if p.FunctionCall != nil {
		return json.Marshal(struct {
			FunctionCall *GeminiFunctionCall `json:"functionCall"`
		}{p.FunctionCall})
	}
	if p.FunctionResponse != nil {
		return json.Marshal(struct {
			FunctionResponse *GeminiFunctionResponse `json:"functionResponse"`
		}{p.FunctionResponse})
	}
	if len(p.Raw) > 0 {
		return p.Raw, nil
	}
	return []byte(`{}`), nil
}

// GeminiInlineData 是 base64 内联载荷（图片/音频等）。
type GeminiInlineData struct {
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data"`
}

// GeminiGeneration 是采样参数集。
type GeminiGeneration struct {
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"topP,omitempty"`
	TopK          *int     `json:"topK,omitempty"`
	MaxOutputTok  *int     `json:"maxOutputTokens,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`
	// 结构化输出：responseMimeType=application/json +
	// responseSchema（OpenAPI 子集），与 IR 的 response_format 互译。
	ResponseMimeType string          `json:"responseMimeType,omitempty"`
	ResponseSchema   json.RawMessage `json:"responseSchema,omitempty"`
}

// UnmarshalJSON 兼容 snake_case 原名。
func (g *GeminiGeneration) UnmarshalJSON(data []byte) error {
	type alias GeminiGeneration
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*g = GeminiGeneration(a)
	var snake struct {
		TopP             *float64        `json:"top_p"`
		TopK             *int            `json:"top_k"`
		MaxOutputTok     *int            `json:"max_output_tokens"`
		ResponseMimeType string          `json:"response_mime_type"`
		ResponseSchema   json.RawMessage `json:"response_schema"`
	}
	if err := json.Unmarshal(data, &snake); err != nil {
		return err
	}
	if g.TopP == nil {
		g.TopP = snake.TopP
	}
	if g.TopK == nil {
		g.TopK = snake.TopK
	}
	if g.MaxOutputTok == nil {
		g.MaxOutputTok = snake.MaxOutputTok
	}
	if g.ResponseMimeType == "" {
		g.ResponseMimeType = snake.ResponseMimeType
	}
	if len(g.ResponseSchema) == 0 {
		g.ResponseSchema = snake.ResponseSchema
	}
	return nil
}

// FromGeminiGenerateContent 把入站请求归一化为 IR。inlineData 图片
// 映射为 IR 的 data-URI image_url，音频映射为 input_audio；top_k 无
// IR 对应字段，进显式降级清单。工具面（tools/toolConfig/
// functionCall/functionResponse）双向互译。
func FromGeminiGenerateContent(model string, in *GeminiRequest, stream bool) (*schema.UnifiedRequest, []string) {
	req := &schema.UnifiedRequest{Model: model, Stream: stream}
	if in.SystemInstruction != nil {
		if msgs := geminiContentsToMessages("system", *in.SystemInstruction); len(msgs) > 0 {
			req.Messages = append(req.Messages, msgs[0])
		}
	}
	for _, c := range in.Contents {
		role := "user"
		switch strings.ToLower(c.Role) {
		case "model":
			role = schema.RoleAssistant
		case "system":
			role = schema.RoleSystem
		}
		req.Messages = append(req.Messages, geminiContentsToMessages(role, c)...)
	}
	req.Tools = geminiToolsFromWire(in.Tools)
	req.ToolChoice = geminiToolConfigFromWire(in.ToolConfig)
	var degraded []string
	if gc := in.GenerationConfig; gc != nil {
		req.Temperature, req.TopP = gc.Temperature, gc.TopP
		req.MaxTokens, req.Stop = gc.MaxOutputTok, gc.StopSequences
		if gc.TopK != nil {
			degraded = append(degraded, "top_k")
		}
		// 结构化输出：归一为 IR 的 OpenAI 形 response_format，
		// 跨协议上游（openai_compat/gemini）都能续接语义。
		if rf := responseFormatFromGemini(gc.ResponseMimeType, gc.ResponseSchema); rf != nil {
			req.ResponseFormat = rf
		}
	}
	if len(in.Tools) > 0 && len(in.Tools[0].FunctionDeclarations) == 0 {
		degraded = append(degraded, "tools") // 非函数声明工具型（googleSearch 等）
	}
	return req, degraded
}

// geminiContentsToMessages 把一条 Gemini 消息映射为 0..n 条 IR 消息：
// functionCall parts 提为 ToolCalls；functionResponse parts 拆为独立
// tool 角色消息（多工具结果各自成条）。
func geminiContentsToMessages(role string, c GeminiContent) []schema.Message {
	var out []schema.Message
	m := schema.Message{Role: role}
	for _, p := range c.Parts {
		switch {
		case p.Text != "":
			m.Content.Parts = append(m.Content.Parts, schema.Part{Type: schema.PartText, Text: p.Text})
		case p.InlineData != nil && strings.HasPrefix(p.InlineData.MimeType, "image/"):
			m.Content.Parts = append(m.Content.Parts, schema.Part{Type: schema.PartImageURL, ImageURL: &schema.ImageURL{
				URL: "data:" + p.InlineData.MimeType + ";base64," + p.InlineData.Data,
			}})
		case p.InlineData != nil && strings.HasPrefix(p.InlineData.MimeType, "audio/"):
			m.Content.Parts = append(m.Content.Parts, schema.Part{Type: schema.PartInputAudio, InputAudio: &schema.InputAudio{
				Data: p.InlineData.Data, Format: strings.TrimPrefix(p.InlineData.MimeType, "audio/"),
			}})
		case p.FunctionCall != nil:
			m.ToolCalls = append(m.ToolCalls, ToolCallFromGemini(*p.FunctionCall))
		case p.FunctionResponse != nil:
			out = append(out, toolMessageFromGemini(*p.FunctionResponse))
		case len(p.Raw) > 0:
			m.Content.Parts = append(m.Content.Parts, schema.Part{Type: "gemini_raw", Raw: p.Raw})
		}
	}
	if len(m.Content.Parts) > 0 || len(m.ToolCalls) > 0 {
		out = append(out, m)
	}
	return out
}
