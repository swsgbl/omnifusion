// gemini_tools.go 承载 Gemini 工具面互译（M3.3）：tools 数组
// （functionDeclarations）、toolConfig（functionCallingConfig）与
// parts 内的 functionCall / functionResponse 双向映射。
package translate

import (
	"encoding/json"
	"strings"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// GeminiTools 是 tools 数组元素（googleSearch 等其余工具型不建模，
// 入站丢失进降级清单由上层可见）。
type GeminiTools struct {
	FunctionDeclarations []GeminiFunctionDecl `json:"functionDeclarations,omitempty"`
}

// UnmarshalJSON 兼容 proto 原名 function_declarations。
func (t *GeminiTools) UnmarshalJSON(data []byte) error {
	type alias GeminiTools
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*t = GeminiTools(a)
	var snake struct {
		FunctionDeclarations []GeminiFunctionDecl `json:"function_declarations"`
	}
	if err := json.Unmarshal(data, &snake); err != nil {
		return err
	}
	if t.FunctionDeclarations == nil {
		t.FunctionDeclarations = snake.FunctionDeclarations
	}
	return nil
}

// GeminiFunctionDecl 是一条函数声明（parameters 即 JSON Schema）。
type GeminiFunctionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// GeminiToolConfig 是 toolConfig 字段。
type GeminiToolConfig struct {
	FunctionCallingConfig *GeminiFunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

// UnmarshalJSON 兼容 proto 原名 tool_config。
func (t *GeminiToolConfig) UnmarshalJSON(data []byte) error {
	type alias GeminiToolConfig
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*t = GeminiToolConfig(a)
	var snake struct {
		FunctionCallingConfig *GeminiFunctionCallingConfig `json:"function_calling_config"`
	}
	if err := json.Unmarshal(data, &snake); err != nil {
		return err
	}
	if t.FunctionCallingConfig == nil {
		t.FunctionCallingConfig = snake.FunctionCallingConfig
	}
	return nil
}

// GeminiFunctionCallingConfig 是函数调用模式与白名单。
type GeminiFunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"` // AUTO/ANY/NONE
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

// UnmarshalJSON 兼容 proto 原名 allowed_function_names。
func (c *GeminiFunctionCallingConfig) UnmarshalJSON(data []byte) error {
	type alias GeminiFunctionCallingConfig
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*c = GeminiFunctionCallingConfig(a)
	var snake struct {
		AllowedFunctionNames []string `json:"allowed_function_names"`
	}
	if err := json.Unmarshal(data, &snake); err != nil {
		return err
	}
	if c.AllowedFunctionNames == nil {
		c.AllowedFunctionNames = snake.AllowedFunctionNames
	}
	return nil
}

// GeminiFunctionCall 是模型发起的调用（part 内字段，args 是对象）。
type GeminiFunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// GeminiFunctionResponse 是调用结果回传（part 内字段，response 必为对象）。
type GeminiFunctionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

// geminiToolsFromWire 收集入站 tools 里的全部函数声明。
func geminiToolsFromWire(tools []GeminiTools) []schema.Tool {
	var out []schema.Tool
	for _, t := range tools {
		for _, d := range t.FunctionDeclarations {
			out = append(out, schema.Tool{
				Type: "function",
				Function: schema.ToolFunction{
					Name: d.Name, Description: d.Description, Parameters: d.Parameters,
				},
			})
		}
	}
	return out
}

// geminiToolsToWire 渲染 IR tools 为单元素 tools 数组。
func geminiToolsToWire(tools []schema.Tool) []GeminiTools {
	if len(tools) == 0 {
		return nil
	}
	decls := make([]GeminiFunctionDecl, 0, len(tools))
	for _, t := range tools {
		decls = append(decls, GeminiFunctionDecl{
			Name: t.Function.Name, Description: t.Function.Description,
			Parameters: t.Function.Parameters,
		})
	}
	return []GeminiTools{{FunctionDeclarations: decls}}
}

// geminiToolConfigFromWire 解析 toolConfig；AUTO/未知归 nil（IR 默认）。
func geminiToolConfigFromWire(tc *GeminiToolConfig) *schema.ToolChoice {
	if tc == nil || tc.FunctionCallingConfig == nil {
		return nil
	}
	fcc := tc.FunctionCallingConfig
	switch fcc.Mode {
	case "NONE":
		return &schema.ToolChoice{Mode: schema.ToolChoiceNone}
	case "ANY":
		if len(fcc.AllowedFunctionNames) == 1 {
			return &schema.ToolChoice{
				Mode: schema.ToolChoiceFunction, Function: fcc.AllowedFunctionNames[0],
			}
		}
		return &schema.ToolChoice{Mode: schema.ToolChoiceRequired}
	case "AUTO":
		return &schema.ToolChoice{Mode: schema.ToolChoiceAuto}
	}
	return nil
}

// geminiToolConfigToWire 渲染 IR tool_choice；auto 是 Gemini 默认，
// 省略 toolConfig。
func geminiToolConfigToWire(tc *schema.ToolChoice) *GeminiToolConfig {
	if tc == nil {
		return nil
	}
	switch tc.Mode {
	case schema.ToolChoiceNone:
		return &GeminiToolConfig{FunctionCallingConfig: &GeminiFunctionCallingConfig{Mode: "NONE"}}
	case schema.ToolChoiceRequired:
		return &GeminiToolConfig{FunctionCallingConfig: &GeminiFunctionCallingConfig{Mode: "ANY"}}
	case schema.ToolChoiceFunction:
		return &GeminiToolConfig{FunctionCallingConfig: &GeminiFunctionCallingConfig{
			Mode: "ANY", AllowedFunctionNames: []string{tc.Function}}}
	}
	return nil
}

// ToolCallFromGemini 把 functionCall part 映射为 IR ToolCall：
// ID 缺省用 Name（Gemini 旧口径无 id）；args 对象字符串化。
func ToolCallFromGemini(fc GeminiFunctionCall) schema.ToolCall {
	id := fc.ID
	if id == "" {
		id = fc.Name
	}
	args := fc.Args
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	return schema.ToolCall{
		ID: id, Type: "function",
		Function: schema.ToolCallFunction{Name: fc.Name, Arguments: string(args)},
	}
}

// toolMessageFromGemini 把 functionResponse part 映射为 IR tool 消息：
// name 当 tool_call_id，response 对象整体作为结果文本。
func toolMessageFromGemini(fr GeminiFunctionResponse) schema.Message {
	resp := fr.Response
	if len(resp) == 0 {
		resp = json.RawMessage("{}")
	}
	return schema.Message{
		Role:       schema.RoleTool,
		ToolCallID: fr.Name,
		Content:    schema.NewTextContent(string(resp)),
	}
}

// geminiResponsePayload 把 IR 工具结果文本归为 response 对象：
// 合法 JSON 对象直通，否则包 {"result": text}（Google 官方兼容口径）。
func geminiResponsePayload(text string) json.RawMessage {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "{") && json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	raw, _ := json.Marshal(map[string]string{"result": text})
	return raw
}

// geminiArgsOf 归一 arguments 字符串为合法 args 载荷（空/无效补 {}）。
func geminiArgsOf(args string) json.RawMessage {
	raw := json.RawMessage(args)
	if len(raw) == 0 || !json.Valid(raw) {
		return json.RawMessage("{}")
	}
	return raw
}

// geminiToolNameIndex 扫描消息历史建 tool_call id → 函数名映射，
// 供出站 functionResponse 解析 name（Gemini 只认 name 不认 id）。
func geminiToolNameIndex(msgs []schema.Message) map[string]string {
	idx := map[string]string{}
	for _, m := range msgs {
		if m.Role != schema.RoleAssistant {
			continue
		}
		for _, c := range m.ToolCalls {
			idx[c.ID] = c.Function.Name
		}
	}
	return idx
}
