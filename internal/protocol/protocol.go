// Package protocol 定义 OmniFusion 的协议内核抽象：四个入站协议
// （OpenAI / Anthropic / Gemini / Responses）统一归一化为
// Normalize → Policy → Route → Execute → Translate 流水线的中立形态。
//
// Phase 1 垂直切片只落接口与分类器（契约测试锁死 JSON 形状与错误映射），
// 不改变任何既有 handler 行为；既有路径迁移在后续切片逐协议进行。
package protocol

import "encoding/json"

// Protocol 标识请求来自哪个入站协议族。
type Protocol string

const (
	ProtocolOpenAI    Protocol = "openai"    // POST /v1/chat/completions
	ProtocolResponses Protocol = "responses" // POST /v1/responses
	ProtocolAnthropic Protocol = "anthropic" // POST /v1/messages
	ProtocolGemini    Protocol = "gemini"    // POST /v1beta/models/*
)

// Role 归一化后的消息角色。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall 是一次工具调用的归一化形态（出站适配器负责与各家
// function-call / tool_use / functionCall 的互转）。
type ToolCall struct {
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"` // JSON object；空参为 {}
}

// ToolResult 是工具执行结果的归一化形态。
type ToolResult struct {
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error,omitempty"`
}

// Message 归一化消息：文本主体 + 可选工具调用/结果。多模态部分
// （图片/音频）在 Phase 1 之后再扩展（届时同步扩 CapabilityMatrix）。
type Message struct {
	Role       Role        `json:"role"`
	Content    string      `json:"content"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`  // assistant 发起
	ToolResult *ToolResult `json:"tool_result,omitempty"` // tool 角色回填
}

// ToolSchema 归一化的工具声明（JSON Schema 形态，与 OpenAI 一致）。
type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"` // JSON Schema
}

// Usage 归一化 token 用量。
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// NormalizedRequest 是所有入站协议归一化后的请求形态。
// 字段保持最小完备：出站适配器从它生成各家 wire format；
// 入站归一化器把各家 wire format 折叠进它。
type NormalizedRequest struct {
	Protocol      Protocol     `json:"protocol"`
	Model         string       `json:"model"`                    // 客户端请求的模型别名（@quality 等指令原样保留）
	ResolvedModel string       `json:"resolved_model,omitempty"` // 路由决议后的真实模型 id
	System        string       `json:"system,omitempty"`
	Messages      []Message    `json:"messages"`
	Tools         []ToolSchema `json:"tools,omitempty"`
	Stream        bool         `json:"stream,omitempty"`
	Temperature   *float64     `json:"temperature,omitempty"`
	TopP          *float64     `json:"top_p,omitempty"`
	MaxTokens     *int         `json:"max_tokens,omitempty"`
	// RequestID 由 server 层注入，用于贯穿 trace/audit/replay。
	RequestID string `json:"request_id,omitempty"`
}

// FinishReason 归一化结束原因。
type FinishReason string

const (
	FinishStop     FinishReason = "stop"
	FinishLength   FinishReason = "length"
	FinishToolCall FinishReason = "tool_calls"
	FinishError    FinishReason = "error"
)

// NormalizedResponse 是出站适配器执行后的归一化响应（非流式形态；
// 流式在 StreamEvent 层处理，final materialized result 亦折叠回此形态）。
type NormalizedResponse struct {
	Text          string       `json:"text,omitempty"`
	ToolCalls     []ToolCall   `json:"tool_calls,omitempty"`
	FinishReason  FinishReason `json:"finish_reason"`
	Usage         Usage        `json:"usage"`
	UpstreamModel string       `json:"upstream_model,omitempty"`
}

// StreamEvent 归一化流式事件：出站适配器把各家 SSE 折叠为事件序列，
// 入站翻译器再展开为目标协议的 SSE 帧。首 chunk 与终止事件的语义
// 由各适配器保证（Phase 1 契约测试锁死，见 stream_test）。
type StreamEvent struct {
	Kind      string       `json:"kind"` // "text" | "tool_call_delta" | "finish" | "error"
	Delta     string       `json:"delta,omitempty"`
	ToolCall  *ToolCall    `json:"tool_call,omitempty"`
	Finish    FinishReason `json:"finish,omitempty"`
	ErrorCode string       `json:"error_code,omitempty"`
	Message   string       `json:"message,omitempty"`
}
