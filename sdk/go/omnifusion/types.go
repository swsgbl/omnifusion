// types.go 是 SDK 的请求/响应结构——OpenAI 兼容面的子集（网关透传
// 上游形状；SDK 只建模稳定字段，未知字段经 json 忽略）。
package omnifusion

// Message 是一条对话消息（role: system|user|assistant|tool）。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest 是 /v1/chat/completions 请求。Model 支持网关指令
// （@fusion/@smart/@combo:NAME 等）与别名。
type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
	Stop        []string  `json:"stop,omitempty"`
	User        string    `json:"user,omitempty"`
}

// clone 拷贝请求（Chat 强制非流式不污染调用方结构）。
func (r *ChatRequest) clone() *ChatRequest {
	q := *r
	q.Messages = append([]Message(nil), r.Messages...)
	q.Stop = append([]string(nil), r.Stop...)
	return &q
}

// Usage 是用量口径（上游 usage 尽力而为；@fusion 为扇出+Judge 求和）。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Choice 是非流式响应的一个候选。
type Choice struct {
	Index        int     `json:"index"`
	FinishReason string  `json:"finish_reason"`
	Message      Message `json:"message"`
}

// ChatResponse 是非流式补全结果。
type ChatResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`

	cacheHit bool
}

// CacheHit 报告本次响应是否命中网关语义缓存（TTFT<10ms 路径）。
func (r *ChatResponse) CacheHit() bool { return r.cacheHit }

// Text 返回首个候选的回复文本（便捷方法）。
func (r *ChatResponse) Text() string {
	if len(r.Choices) == 0 {
		return ""
	}
	return r.Choices[0].Message.Content
}

// ModelInfo 是目录快照里的一条模型。
type ModelInfo struct {
	ID string `json:"id"`
}

// Delta 是流式分片的增量内容。
type Delta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// StreamChoice 是流式分片的一个候选。
type StreamChoice struct {
	Index        int    `json:"index"`
	Delta        Delta  `json:"delta"`
	FinishReason string `json:"finish_reason,omitempty"`
}

// Chunk 是一个 SSE 分片（data: {...} 解析结果）。
type Chunk struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []StreamChoice `json:"choices"`
}
