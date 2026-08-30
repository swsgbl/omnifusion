// response_format.go 是 结构化输出互译的共享面：IR 采用 OpenAI
// 形 response_format（{"type":"json_object"} / {"type":"json_schema",
// "json_schema":{"schema":{...}}}），Gemini 面是 generationConfig 的
// responseMimeType + responseSchema（OpenAPI 3.0 schema 子集）。
// Anthropic Messages API 无原生结构化输出——出站丢弃并进显式降级
// 清单（ 3.6：不支持的上游显式降级标记，禁止静默丢弃）。
package translate

import (
	"encoding/json"
)

// geminiJSONMime 是 Gemini 结构化输出的 responseMimeType 值。
const geminiJSONMime = "application/json"

// geminiUnsupportedSchemaKeys 是 JSON Schema 键中 Gemini OpenAPI 子集
// 不收、出站前必须剥离的（带了会被上游 400 拒掉）。
var geminiUnsupportedSchemaKeys = map[string]bool{
	"$schema": true, "additionalProperties": true, "strict": true,
}

// responseFormatSpec 是解析后的结构化输出意图（Gemini 面中间形）。
type responseFormatSpec struct {
	mime   string
	schema json.RawMessage // nil = 仅 json_object，无 schema 约束
}

// parseResponseFormat 把 IR.ResponseFormat（OpenAI 形）解析为 Gemini
// 面。ok=false 表示无法理解（未知 type、坏 JSON、json_schema 缺
// schema），调用方降级丢弃。
func parseResponseFormat(raw json.RawMessage) (responseFormatSpec, bool) {
	var wire struct {
		Type       string `json:"type"`
		JSONSchema *struct {
			Schema json.RawMessage `json:"schema"`
		} `json:"json_schema"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return responseFormatSpec{}, false
	}
	switch wire.Type {
	case "json_object":
		return responseFormatSpec{mime: geminiJSONMime}, true
	case "json_schema":
		if wire.JSONSchema == nil || len(wire.JSONSchema.Schema) == 0 {
			return responseFormatSpec{}, false
		}
		return responseFormatSpec{
			mime:   geminiJSONMime,
			schema: stripUnsupportedSchemaKeys(wire.JSONSchema.Schema),
		}, true
	}
	return responseFormatSpec{}, false
}

// responseFormatFromGemini 合成 IR.ResponseFormat（OpenAI 形）：
// application/json + schema → json_schema；仅 mime → json_object；
// 其他 mime（text/plain 等）无结构化语义，返回 nil。
func responseFormatFromGemini(mime string, schema json.RawMessage) json.RawMessage {
	if mime != geminiJSONMime {
		return nil
	}
	out := map[string]any{"type": "json_object"}
	if len(schema) > 0 {
		out = map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"schema": schema},
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return b
}

// stripUnsupportedSchemaKeys 递归剥离 Gemini 不收的 JSON Schema 键，
// 返回可安全作为 responseSchema 的副本（原值不动）。
func stripUnsupportedSchemaKeys(raw json.RawMessage) json.RawMessage {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw // 非 JSON Schema 对象：原样交给上游判错
	}
	cleaned := stripKeys(v)
	b, err := json.Marshal(cleaned)
	if err != nil {
		return raw
	}
	return b
}

func stripKeys(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if geminiUnsupportedSchemaKeys[k] {
				continue
			}
			out[k] = stripKeys(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = stripKeys(val)
		}
		return out
	default:
		return v
	}
}
