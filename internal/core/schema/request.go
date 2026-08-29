package schema

import "encoding/json"

// UnifiedRequest 是入站请求归一化后的中枢 IR（OpenAI 形）。
// 指针字段区分「未设置」与「零值」，翻译时不得把未设置渲染成零值。
type UnifiedRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream,omitempty"`

	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	MaxTokens   *int     `json:"max_tokens,omitempty"`
	Stop        []string `json:"stop,omitempty"`

	Tools      []Tool          `json:"tools,omitempty"`
	ToolChoice *ToolChoice     `json:"tool_choice,omitempty"`
	StreamOpts json.RawMessage `json:"stream_options,omitempty"`

	ResponseFormat json.RawMessage `json:"response_format,omitempty"`
	Seed           *int64          `json:"seed,omitempty"`
	User           string          `json:"user,omitempty"`

	// Extra 保留未建模的顶层字段（按原序透传给支持它们的上游）。
	Extra ExtraFields `json:"-"`
}

// Tool 是可供模型调用的工具定义（OpenAI function calling 形）。
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction 是工具函数定义。Parameters 保持原始 JSON Schema 不解析。
type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// ToolCall 是模型发起的一次工具调用。Arguments 保持字符串原样
// （OpenAI 线上形态即字符串化 JSON，不做解析以免改变语义）。
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Index    *int             `json:"index,omitempty"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction 承载工具调用的名称与参数。
type ToolCallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// ToolChoice 取值：字符串（auto/none/required）或对象
// {"type":"function","function":{"name":...}}。
type ToolChoice struct {
	Mode     string // "auto" | "none" | "required" | "function"
	Function string // Mode == "function" 时指定的工具名
}

// 常见 ToolChoice 模式。
const (
	ToolChoiceAuto     = "auto"
	ToolChoiceNone     = "none"
	ToolChoiceRequired = "required"
	ToolChoiceFunction = "function"
)

// MarshalJSON 将 ToolChoice 编码为其线上形态。
func (t ToolChoice) MarshalJSON() ([]byte, error) {
	if t.Mode != ToolChoiceFunction {
		return json.Marshal(t.Mode)
	}
	return json.Marshal(struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}{ToolChoiceFunction, struct {
		Name string `json:"name"`
	}{t.Function}})
}

// UnmarshalJSON 接受字符串或对象两种线上形态。
func (t *ToolChoice) UnmarshalJSON(data []byte) error {
	b := skipSpace(data)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		t.Mode = s
		t.Function = ""
		return nil
	}
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	t.Mode = obj.Type
	t.Function = obj.Function.Name
	return nil
}
