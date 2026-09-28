package protocol

import (
	"encoding/json"
	"testing"
)

// 契约测试：锁死归一化形态的 JSON 形状。任何字段重命名/形状变更必须
// 在这里显式失败并同步所有消费方（router/audit/replay）。
func TestNormalizedRequestJSONShape(t *testing.T) {
	temp := 0.7
	max := 4096
	req := NormalizedRequest{
		Protocol:      ProtocolOpenAI,
		Model:         "@quality",
		ResolvedModel: "deepseek-v4-pro",
		System:        "you are helpful",
		Messages: []Message{
			{Role: RoleUser, Content: "hi"},
			{Role: RoleAssistant, Content: "", ToolCalls: []ToolCall{
				{ID: "c1", Name: "get_weather", Arguments: json.RawMessage(`{"city":"sh"}`)},
			}},
			{Role: RoleTool, Content: "sunny", ToolResult: &ToolResult{ToolCallID: "c1", Content: "sunny"}},
		},
		Tools:       []ToolSchema{{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object"}`)}},
		Stream:      true,
		Temperature: &temp,
		MaxTokens:   &max,
		RequestID:   "req-1",
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var back NormalizedRequest
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Model != "@quality" || back.ResolvedModel != "deepseek-v4-pro" ||
		len(back.Messages) != 3 || len(back.Messages[1].ToolCalls) != 1 ||
		back.Messages[2].ToolResult == nil || back.Messages[2].ToolResult.ToolCallID != "c1" ||
		!back.Stream || back.RequestID != "req-1" {
		t.Fatalf("round-trip lost fields: %+v", back)
	}
	// 字段名契约（消费方按这些名字读取）
	for _, key := range []string{`"protocol":"openai"`, `"model":"@quality"`, `"tool_calls"`, `"tool_call_id"`, `"request_id":"req-1"`} {
		if !contains(string(b), key) {
			t.Errorf("JSON missing %s in %s", key, b)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestClassifyStatus(t *testing.T) {
	cases := []struct {
		code int
		want ErrorClass
	}{
		{401, ErrAuth}, {403, ErrAuth},
		{400, ErrBadRequest}, {413, ErrBadRequest}, {422, ErrBadRequest},
		{404, ErrNotFound}, {405, ErrMethod},
		{429, ErrRateLimit},
		{408, ErrTimeout}, {504, ErrTimeout},
		{500, ErrServer}, {502, ErrServer}, {503, ErrServer},
		{529, ErrServer},
		{418, ErrBadRequest},
		{200, ErrUnknown},
	}
	for _, c := range cases {
		if got := ClassifyStatus(c.code); got != c.want {
			t.Errorf("ClassifyStatus(%d) = %s, want %s", c.code, got, c.want)
		}
	}
}

func TestRetryability(t *testing.T) {
	cases := []struct {
		cls  ErrorClass
		want Retryability
	}{
		{ErrAuth, RetryNever},
		{ErrBadRequest, RetryNever},
		{ErrContentPolicy, RetryNever},
		{ErrNotFound, RetryNever},
		{ErrMethod, RetryNever},
		{ErrRateLimit, RetryCooldown},
		{ErrTimeout, RetryFailover},
		{ErrServer, RetryFailover},
		{ErrUnavailable, RetryFailover},
		{ErrUnknown, RetryFailover},
	}
	for _, c := range cases {
		if got := c.cls.Retryability(); got != c.want {
			t.Errorf("%s.Retryability() = %s, want %s", c.cls, got, c.want)
		}
	}
	// 硬性纪律：重试语义必须是显式值，不允许空串漏给调用方。
	for _, cls := range []ErrorClass{ErrAuth, ErrBadRequest, ErrContentPolicy, ErrNotFound,
		ErrMethod, ErrRateLimit, ErrTimeout, ErrServer, ErrUnavailable, ErrUnknown} {
		if cls.Retryability() == "" {
			t.Errorf("%s has empty retryability", cls)
		}
	}
}

func TestCapabilityMatrix(t *testing.T) {
	var nilMatrix *CapabilityMatrix
	if nilMatrix.Supports(CapTools) {
		t.Error("nil matrix must not claim capabilities")
	}
	m := &CapabilityMatrix{Provider: "openrouter", Model: "x", Caps: []Capability{CapTools, CapStream}}
	if !m.Supports(CapTools) || !m.Supports(CapStream) {
		t.Error("declared capabilities missing")
	}
	if m.Supports(CapVision) {
		t.Error("undeclared capability claimed")
	}
	b, _ := json.Marshal(m)
	if !contains(string(b), `"caps":["tools","stream"]`) {
		t.Errorf("capability JSON shape changed: %s", b)
	}
}

func TestStreamEventShape(t *testing.T) {
	ev := StreamEvent{Kind: "text", Delta: "hello"}
	b, _ := json.Marshal(ev)
	if !contains(string(b), `"kind":"text"`) || !contains(string(b), `"delta":"hello"`) {
		t.Errorf("stream event shape changed: %s", b)
	}
	fin := StreamEvent{Kind: "finish", Finish: FinishToolCall}
	b2, _ := json.Marshal(fin)
	if !contains(string(b2), `"finish":"tool_calls"`) {
		t.Errorf("finish shape changed: %s", b2)
	}
}
