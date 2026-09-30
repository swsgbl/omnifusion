// genai.go 是 GenAI 语义约定关联字段（蓝图 Phase 9：OpenTelemetry
// GenAI semantic conventions 命名的统一关联面）。零遥测是硬承诺——
// 本文件只定义本地结构化日志的标准字段名与构造器，不导出任何远端；
// 命名对齐 semconv 是为可读性与未来可接 OTLP 的兼容性。
//
// 纪律（蓝图 §10）：字段只含 ID/名称/枚举——prompt、output、
// secret、任何用户原文永不进入关联面；是否记录载荷由独立的
// redaction+policy 决定，与本构造器无关。
package obs

// OTel GenAI semconv 字段名（稳定契约：改名=破坏日志消费方）。
const (
	FieldRequestID      = "gen_ai.request.id"
	FieldOperation      = "gen_ai.operation.name"
	FieldSystem         = "gen_ai.system"
	FieldRequestModel   = "gen_ai.request.model"
	FieldResponseModel  = "gen_ai.response.model"
	FieldConversationID = "gen_ai.conversation.id"
	FieldTaskID         = "gen_ai.task.id"
	FieldToolName       = "gen_ai.tool.name"
	FieldToolCallID     = "gen_ai.tool_call.id"
)

// 操作名（semconv 常用形态；本网关的协议面映射）。
const (
	OpChatCompletions = "chat.completions"
	OpA2ASend         = "a2a.send_message"
	OpA2AStream       = "a2a.send_streaming_message"
	OpA2ATaskGet      = "a2a.get_task"
	OpA2ATaskCancel   = "a2a.cancel_task"
	OpA2ATaskList     = "a2a.list_tasks"
	OpA2ASubscribe    = "a2a.subscribe_task"
)

// GenAICorrelation 是一次 GenAI 操作的关联字段集：请求身份
// （request/conversation/task）、路由事实（system=provider、请求与
// 决议模型）、工具身份（tool/tool_call）。零值可用；Fields 只吐
// 非空字段（消费方按存在性解读）。
type GenAICorrelation struct {
	Operation      string
	System         string // 胜出 provider 名（semconv gen_ai.system）
	RequestModel   string // 客户端请求的模型/别名
	ResponseModel  string // 决议后的具体模型（resolved）
	RequestID      string
	ConversationID string // 会话亲和 ID（sticky session）
	TaskID         string
	ToolName       string
	ToolCallID     string
}

// Fields 返回 slog 键值对（nil 安全；空字段跳过）。
func (c *GenAICorrelation) Fields() []any {
	if c == nil {
		return nil
	}
	f := make([]any, 0, 18)
	add := func(k, v string) {
		if v != "" {
			f = append(f, k, v)
		}
	}
	add(FieldOperation, c.Operation)
	add(FieldSystem, c.System)
	add(FieldRequestModel, c.RequestModel)
	add(FieldResponseModel, c.ResponseModel)
	add(FieldRequestID, c.RequestID)
	add(FieldConversationID, c.ConversationID)
	add(FieldTaskID, c.TaskID)
	add(FieldToolName, c.ToolName)
	add(FieldToolCallID, c.ToolCallID)
	return f
}
