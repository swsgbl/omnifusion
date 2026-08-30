package schema

import (
	"encoding/json"
	"io"
)

// FinishReason 取值（OpenAI 口径）。
const (
	FinishStop        = "stop"
	FinishLength      = "length"
	FinishToolCalls   = "tool_calls"
	FinishContentFilt = "content_filter"
)

// Response 是非流式聚合响应（chat.completion）。
type Response struct {
	ID                string           `json:"id"`
	Object            string           `json:"object"`
	Created           int64            `json:"created"`
	Model             string           `json:"model"`
	Choices           []ResponseChoice `json:"choices"`
	Usage             *Usage           `json:"usage,omitempty"`
	SystemFingerprint string           `json:"system_fingerprint,omitempty"`
	ServiceTier       string           `json:"service_tier,omitempty"`

	// ProviderName 记录产生本响应的上游（网关内部元数据，不上线）。
	ProviderName string `json:"-"`
}

// UnifiedResponse 是 冻结接口使用的名称。
type UnifiedResponse = Response

// ResponseChoice 是非流式响应中的一条候选。
type ResponseChoice struct {
	Index        int             `json:"index"`
	Message      Message         `json:"message"`
	FinishReason string          `json:"finish_reason"`
	Logprobs     json.RawMessage `json:"logprobs,omitempty"`
}

// Chunk 是流式响应的单个事件载荷（chat.completion.chunk）。
type Chunk struct {
	ID                string        `json:"id"`
	Object            string        `json:"object"`
	Created           int64         `json:"created"`
	Model             string        `json:"model"`
	Choices           []ChunkChoice `json:"choices"`
	Usage             *Usage        `json:"usage,omitempty"`
	SystemFingerprint string        `json:"system_fingerprint,omitempty"`

	// ProviderName 记录产生本事件的上游（网关内部元数据，不上线）。
	ProviderName string `json:"-"`
}

// ChunkChoice 是流式事件中的一条候选；Delta 为增量。
type ChunkChoice struct {
	Index        int             `json:"index"`
	Delta        Message         `json:"delta"`
	FinishReason string          `json:"finish_reason"`
	Logprobs     json.RawMessage `json:"logprobs,omitempty"`
}

// Usage 是 token 用量统计。
type Usage struct {
	PromptTokens     int             `json:"prompt_tokens"`
	CompletionTokens int             `json:"completion_tokens"`
	TotalTokens      int             `json:"total_tokens"`
	Details          json.RawMessage `json:"details,omitempty"`
}

// NewResponse 构造最小合法的非流式响应骨架。
func NewResponse(id, model string, created int64) *Response {
	return &Response{
		ID:      id,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []ResponseChoice{},
	}
}

// NewChunk 构造最小合法的流式事件骨架。
func NewChunk(id, model string, created int64) *Chunk {
	return &Chunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []ChunkChoice{},
	}
}

// NewResponseFromReader 解析上游非流式聚合响应体。未知字段按
// encoding/json 默认行为忽略（响应面不做透传建模， 口径）。
func NewResponseFromReader(r io.Reader) (*Response, error) {
	var resp Response
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
