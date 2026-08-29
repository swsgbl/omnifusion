// Package a2a 实现 Agent2Agent (A2A) 协议 v1.0 的线上类型与转换
// （docs/04-架构设计 §3 数据面第四协议面）。
//
// 依据：a2aproject/A2A specification v1.0（JSON-RPC 2.0 绑定，camelCase
// 字段；Part 判别式 = 成员名本身，v1.0 弃用 kind 字段）。
package a2a

import "encoding/json"

// ProtocolVersion 是本实现声明与接受的 A2A 大小版本（协议版本协商以
// AgentCard.supportedInterfaces[].protocolVersion 为准）。
const ProtocolVersion = "1.0"

// JSON-RPC 2.0 与 A2A 错误码（spec §5.4 映射表）。
const (
	CodeParse          = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603

	CodeTaskNotFound          = -32001
	CodeNotCancelable         = -32002
	CodePushNotSupported      = -32003
	CodeUnsupportedOperation  = -32004
	CodeContentTypeNotSupport = -32005
	CodeInvalidAgentResponse  = -32006
)

// TaskState 线上值（proto 枚举名直出）。
const (
	StateWorking    = "TASK_STATE_WORKING"
	StateCompleted  = "TASK_STATE_COMPLETED"
	StateFailed     = "TASK_STATE_FAILED"
	StateCanceled   = "TASK_STATE_CANCELED"
	StateInputRequr = "TASK_STATE_INPUT_REQUIRED"
)

// Role 线上值。
const (
	RoleUser  = "ROLE_USER"
	RoleAgent = "ROLE_AGENT"
)

// Request 是 JSON-RPC 2.0 请求信封。ID 保留原始 JSON 原样回显。
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response 是 JSON-RPC 2.0 响应信封（result 与 error 互斥）。
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError 是 JSON-RPC 错误对象。
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Part 是消息内容片段。v1.0 判别式 = 成员名本身：
// 文本 {"text"}、文件 {"raw"|"url","filename","mediaType"}、
// 结构化 {"data","mediaType"}。
type Part struct {
	Text      string          `json:"text,omitempty"`
	Raw       string          `json:"raw,omitempty"`
	URL       string          `json:"url,omitempty"`
	Filename  string          `json:"filename,omitempty"`
	MediaType string          `json:"mediaType,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// TextPart 构造文本片段。
func TextPart(s string) Part { return Part{Text: s} }

// Message 是 A2A 消息（camelCase 线上形态）。
type Message struct {
	MessageID string          `json:"messageId,omitempty"`
	TaskID    string          `json:"taskId,omitempty"`
	ContextID string          `json:"contextId,omitempty"`
	Role      string          `json:"role"`
	Parts     []Part          `json:"parts"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

// TaskStatus 是任务状态（state + 可选状态消息 + 时间戳）。
type TaskStatus struct {
	State     string   `json:"state"`
	Message   *Message `json:"message,omitempty"`
	Timestamp string   `json:"timestamp,omitempty"`
}

// Artifact 是任务产物（本实现用于流式增量文本）。
type Artifact struct {
	ArtifactID string `json:"artifactId,omitempty"`
	Name       string `json:"name,omitempty"`
	Parts      []Part `json:"parts"`
}

// Task 是任务对象。本网关为无状态代理：仅在流式生命周期内构造，
// 不落盘、事后不可查询（GetTask → TaskNotFound）。
type Task struct {
	ID        string          `json:"id"`
	ContextID string          `json:"contextId"`
	Status    TaskStatus      `json:"status"`
	Artifacts []Artifact      `json:"artifacts,omitempty"`
	History   []Message       `json:"history,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

// TaskStatusUpdateEvent 是流式状态更新事件。v1.0 已删除 0.3 的
// final 字段：流的终止信号 = 连接关闭 + 终态 state。
type TaskStatusUpdateEvent struct {
	TaskID    string     `json:"taskId"`
	ContextID string     `json:"contextId"`
	Status    TaskStatus `json:"status"`
}

// TaskArtifactUpdateEvent 是流式产物增量事件。
type TaskArtifactUpdateEvent struct {
	TaskID    string   `json:"taskId"`
	ContextID string   `json:"contextId"`
	Artifact  Artifact `json:"artifact"`
	Append    bool     `json:"append,omitempty"`
	LastChunk bool     `json:"lastChunk,omitempty"`
}

// StreamResponse 是流式事件的 oneof 封装（task/message/statusUpdate/
// artifactUpdate 恰出现其一）。
type StreamResponse struct {
	Task           *Task                    `json:"task,omitempty"`
	Message        *Message                 `json:"message,omitempty"`
	StatusUpdate   *TaskStatusUpdateEvent   `json:"statusUpdate,omitempty"`
	ArtifactUpdate *TaskArtifactUpdateEvent `json:"artifactUpdate,omitempty"`
}

// SendMessageResponse 是非流式 SendMessage 的 oneof 载荷。本网关
// v1 采用 Message-only 模式（简单交互不建任务）。
type SendMessageResponse struct {
	Task    *Task    `json:"task,omitempty"`
	Message *Message `json:"message,omitempty"`
}

// SendMessageParams 是 SendMessage / SendStreamingMessage 的 params 形态。
type SendMessageParams struct {
	Message  Message         `json:"message"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}
