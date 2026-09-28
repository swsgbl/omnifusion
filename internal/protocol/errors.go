package protocol

import "net/http"

// ErrorClass 把上游/协议层失败归入统一类别，是重试、熔断、冷却与
// policy 决策的单一事实源。出站适配器必须把每类失败映射到这里；
// 不允许各路径自带私有分类。
type ErrorClass string

const (
	ErrAuth          ErrorClass = "auth"           // 401/403：密钥无效或无权限
	ErrBadRequest    ErrorClass = "bad_request"    // 400/413/422：请求本身不合法
	ErrContentPolicy ErrorClass = "content_policy" // 上游内容策略拒绝
	ErrNotFound      ErrorClass = "not_found"      // 404：模型/端点不存在
	ErrMethod        ErrorClass = "method"         // 405：方法不允许
	ErrRateLimit     ErrorClass = "rate_limit"     // 429：限流/配额
	ErrTimeout       ErrorClass = "timeout"        // 上游超时
	ErrServer        ErrorClass = "server"         // 5xx：上游服务错误
	ErrUnavailable   ErrorClass = "unavailable"    // 连接拒绝/DNS/网络不可达
	ErrUnknown       ErrorClass = "unknown"
)

// Retryability 是错误的显式重试语义（施工包硬性纪律：retryability
// 必须显式，不允许调用方按状态码猜）。
type Retryability string

const (
	// RetryNever 不允许重试：重试必然得到同样失败（改请求或改密钥才有意义）。
	RetryNever Retryability = "never"
	// RetryFailover 立即换下一候选：该提供方/模型短期不可用。
	RetryFailover Retryability = "failover"
	// RetryCooldown 换候选并冷却该提供方一个窗口（限流/反复失败）。
	RetryCooldown Retryability = "cooldown"
	// RetryBackoff 同候选退避重试（瞬时抖动）。
	RetryBackoff Retryability = "backoff"
)

// ClassifyStatus 按 HTTP 状态码归类。适配器遇到无法归类的响应体时
// 也应先走状态码归类，再把响应体细节放进错误消息（不含 secret）。
func ClassifyStatus(code int) ErrorClass {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrAuth
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge,
		http.StatusUnprocessableEntity:
		return ErrBadRequest
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusMethodNotAllowed:
		return ErrMethod
	case http.StatusTooManyRequests:
		return ErrRateLimit
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return ErrTimeout
	}
	if code >= 500 {
		return ErrServer
	}
	if code >= 400 {
		return ErrBadRequest
	}
	return ErrUnknown
}

// Retryability 给出该类错误的显式重试语义：
//   - auth/bad_request/content_policy/not_found/method：never（换候选也不该
//     重放同一请求——语义上注定失败；not_found 会随目录刷新自愈）；
//   - rate_limit：cooldown（该提供方冷却一个窗口，立即换候选）；
//   - timeout/server/unavailable：failover（换候选立即试，无需冷却）；
//   - unknown：failover（保守：不惩罚提供方，但不在此处空转）。
func (c ErrorClass) Retryability() Retryability {
	switch c {
	case ErrAuth, ErrBadRequest, ErrContentPolicy, ErrNotFound, ErrMethod:
		return RetryNever
	case ErrRateLimit:
		return RetryCooldown
	case ErrTimeout, ErrServer, ErrUnavailable:
		return RetryFailover
	}
	return RetryFailover
}

// Capability 是提供方/模型能力的强类型位（进入 CapabilityMatrix 与
// 路由硬过滤；字符串形态便于 JSON 序列化进 catalog）。
type Capability string

const (
	CapTools            Capability = "tools"             // 工具调用
	CapStructuredOutput Capability = "structured_output" // JSON schema 输出
	CapVision           Capability = "vision"            // 图片输入
	CapStream           Capability = "stream"            // 流式
	CapReasoning        Capability = "reasoning"         // 推理/思考链
)

// CapabilityMatrix 记录 (provider, model) 的能力集合；路由硬过滤与
// 出站适配器的请求裁剪都从这里取事实。KNOWN-GAP（Phase 0 审计 #4）：
// 现有 catalog 的 capabilities 字段迁移进此类型由后续切片完成。
type CapabilityMatrix struct {
	Provider string       `json:"provider"`
	Model    string       `json:"model"`
	Caps     []Capability `json:"caps"`
}

// Supports 报告是否具备某能力（空矩阵=不确定，返回 false 由调用方
// 决定是否放行——路由硬过滤对 UNKNOWN 的语义另行处理）。
func (m *CapabilityMatrix) Supports(c Capability) bool {
	if m == nil {
		return false
	}
	for _, v := range m.Caps {
		if v == c {
			return true
		}
	}
	return false
}
