package schema

import (
	"bytes"
	"encoding/json"
)

// requestAlias 与 UnifiedRequest 同形，用于避免递归 JSON 方法。
type requestAlias UnifiedRequest

// knownRequestKeys 是 UnifiedRequest 已建模的顶层字段；其余进入 Extra 透传。
var knownRequestKeys = map[string]bool{
	"model": true, "messages": true, "stream": true,
	"temperature": true, "top_p": true, "max_tokens": true, "stop": true,
	"tools": true, "tool_choice": true, "stream_options": true,
	"response_format": true, "seed": true, "user": true,
}

// UnmarshalJSON 解析请求并将未建模字段按原序收进 Extra。
func (r *UnifiedRequest) UnmarshalJSON(data []byte) error {
	var alias requestAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	*r = UnifiedRequest(alias)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	// map 遍历无序；为保原序，按 data 中的键出现顺序收集。
	order := topLevelKeyOrder(data)
	for _, k := range order {
		if knownRequestKeys[k] {
			continue
		}
		if v, ok := raw[k]; ok {
			r.Extra.Set(k, v)
		}
	}
	return nil
}

// MarshalJSON 序列化请求并把 Extra 字段回放进顶层对象。
func (r UnifiedRequest) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(requestAlias(r))
	if err != nil {
		return nil, err
	}
	if r.Extra.Keys() == nil {
		return base, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(base, &obj); err != nil {
		return nil, err
	}
	for _, k := range r.Extra.Keys() {
		if v, ok := r.Extra.Get(k); ok && !knownRequestKeys[k] {
			obj[k] = v
		}
	}
	return json.Marshal(obj)
}

// topLevelKeyOrder 线性扫描顶层对象，返回键的出现顺序（不解析值内部）。
func topLevelKeyOrder(data []byte) []string {
	dec := json.NewDecoder(bytes.NewReader(data))
	var keys []string
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return keys
		}
		if s, ok := tok.(string); ok {
			keys = append(keys, s)
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return keys
		}
	}
	return keys
}
