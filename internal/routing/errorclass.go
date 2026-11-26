// errorclass.go 是 错误分类器：
// 把任意一次 provider 尝试的错误归一化为 ErrorKind，再由 PolicyFor
// 查表给出处理策略。分类只认 typed error（UpstreamError / StreamError）
// 与标准库信号，禁止对字符串做脆弱匹配（body 关键词仅用于区分 429
// 的限流与配额两种语义）。
package routing

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/swsgbl/omnifusion/internal/provider"
)

// ErrorKind 归一化错误类别。前六类对应 的六类错误；
// request_error 是非鉴权 4xx（请求自身问题，不惩罚 provider），
// unknown 兜底。
type ErrorKind string

const (
	KindRateLimit      ErrorKind = "rate_limit"      // 429 且无配额关键词
	KindQuotaExhausted ErrorKind = "quota_exhausted" // 402 或 429+配额关键词
	KindAuthInvalid    ErrorKind = "auth_invalid"    // 401/403
	KindUpstream5xx    ErrorKind = "upstream_5xx"    // >=500
	KindNetOrTimeout   ErrorKind = "net_or_timeout"  // 超时/网络中断（仅退避不冷却）
	KindStreamBroken   ErrorKind = "stream_broken"   // 流中断（读失败/无 [DONE]/坏载荷/流内错误事件）
	KindRequestError   ErrorKind = "request_error"   // 非鉴权 4xx
	KindModelGone      ErrorKind = "model_gone"      // 404/410：模型已下线或本账户无权使用
	KindUnknown        ErrorKind = "unknown"
)

const (
	// RateLimitCooldown 是 rate_limit 的 Connection 冷却时长（消费）。
	RateLimitCooldown = 30 * time.Second
	// AuthInvalidCooldown 是 auth_invalid 的长冷却：key 疑似失效，
	// 人工复核（ofd key validate / 换 key）之前不值得高频重试。
	AuthInvalidCooldown = 30 * time.Minute
	// ModelGoneLockout 是 model_gone 的 Model 锁定时长。模型下线/无权
	// 不会几分钟内自愈，但不做太长：错配 base_url 也表现为逐模型 404，
	// 用户改完配置重启后不该还被旧锁定挡住。
	ModelGoneLockout = 10 * time.Minute
)

// Policy 是某类错误的处理策略，字段对应 处理策略表。
// 只产出策略数据；冷却/锁定/降分的执行在 状态机接入。
type Policy struct {
	// Failover 表示是否切换下一候选。全部类别默认允许（保持 行为）。
	Failover bool
	// Cooldown 是 Connection 层冷却时长；0 表示不冷却。
	Cooldown time.Duration
	// LockoutModel 表示把该 Model 锁定到额度重置点（reset-aware，）。
	// 锁定时长按类别取：quota_exhausted 用 QuotaLockoutDuration，
	// model_gone 用 ModelGoneLockout。
	LockoutModel bool
	// InvalidKey 表示把关联 API key 置为 invalid，需人工处理。
	InvalidKey bool
	// HealthPenalty 表示记一次健康降分（打分路由消费）。
	HealthPenalty bool
}

// policyTable 是 处理策略表的代码形态：
// - rate_limit 冷却 Connection 30s；
// - quota_exhausted 锁定 Model（不冷却连接——连接本身没病）；
// - auth_invalid 长冷却 + 置 invalid；
// - upstream_5xx / stream_broken 降分但不冷却（仅同元组退避，）；
// - net_or_timeout 仅退避：瞬时网络问题不惩罚 provider；
// - model_gone 锁定 Model（2026-11-25 用户实撞：目录里已 410 的模型
//   每个请求都被重试，NVIDIA 一家 27 次原地撞墙）；
// - request_error / unknown 不惩罚（请求自身问题或不明错误）。
var policyTable = map[ErrorKind]Policy{
	KindRateLimit:      {Failover: true, Cooldown: RateLimitCooldown},
	KindQuotaExhausted: {Failover: true, LockoutModel: true},
	KindAuthInvalid:    {Failover: true, Cooldown: AuthInvalidCooldown, InvalidKey: true},
	KindUpstream5xx:    {Failover: true, HealthPenalty: true},
	KindNetOrTimeout:   {Failover: true},
	KindStreamBroken:   {Failover: true, HealthPenalty: true},
	KindModelGone:      {Failover: true, LockoutModel: true},
	KindRequestError:   {Failover: true},
	KindUnknown:        {Failover: true},
}

// PolicyFor 返回某类错误的处理策略；空类别（成功尝试）与未知类别
// 不惩罚。
func PolicyFor(kind ErrorKind) Policy {
	if p, ok := policyTable[kind]; ok {
		return p
	}
	return Policy{Failover: true}
}

// quotaBodyRe 匹配 429 响应体中的配额语义关键词：OpenAI 的
// insufficient_quota、OpenRouter 的 credits/free-models-per-day、
// 各家的 billing/payment/balance 表述。命中即从 rate_limit 改判
// quota_exhausted（锁定 Model 而非冷却连接）。
var quotaBodyRe = regexp.MustCompile(`(?i)quota|billing|credit|payment|balance`)

// Classify 把一次尝试的错误归一化为 ErrorKind。nil（成功尝试）
// 返回空类别；错误经 fmt.Errorf("%w") 包装不影响结果。
func Classify(err error) ErrorKind {
	if err == nil {
		return ""
	}
	var ue *provider.UpstreamError
	if errors.As(err, &ue) {
		return classifyUpstream(ue)
	}
	var se *provider.StreamError
	if errors.As(err, &se) {
		return KindStreamBroken
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return KindNetOrTimeout
	}
	var nerr net.Error
	if errors.As(err, &nerr) { // 含 *url.Error（自带 Timeout/Temporary）
		return KindNetOrTimeout
	}
	var uerr *url.Error
	if errors.As(err, &uerr) { // 兜底：http.Client.Do 的传输错误形态
		return KindNetOrTimeout
	}
	return KindUnknown
}

// classifyUpstream 按 的状态码映射分类上游错误。
func classifyUpstream(ue *provider.UpstreamError) ErrorKind {
	switch {
	case ue.Status == 0:
		// 契约：流内嵌 {"error":...} 事件归一为 Status==0。
		return KindStreamBroken
	case ue.Status == http.StatusTooManyRequests:
		if quotaBodyRe.Match(ue.Body) {
			return KindQuotaExhausted
		}
		return KindRateLimit
	case ue.Status == http.StatusPaymentRequired:
		return KindQuotaExhausted
	case ue.Status == http.StatusUnauthorized:
		return KindAuthInvalid
	case ue.Status == http.StatusForbidden:
		// 403 也常是"钱的问题"而不是凭据：Agnes 等厂商用 403 表达配额/
		// 余额不足（2026-11-25 用户实撞：付费模型 403 把整家冷却 30
		// 分钟，同家好用的免费模型跟着 0ms 502）。命中配额关键词就按
		// 配额锁定模型；区域封锁一类无关键词的仍是 auth_invalid。
		if quotaBodyRe.Match(ue.Body) {
			return KindQuotaExhausted
		}
		return KindAuthInvalid
	case ue.Status == http.StatusNotFound, ue.Status == http.StatusGone:
		// 模型不存在/已下架/本账户无权（"not available for your user
		// group"）：对这个账户它就是没了，再试也是 404——锁模型。
		return KindModelGone
	case ue.Status >= 500:
		return KindUpstream5xx
	case ue.Status >= 400:
		return KindRequestError
	default:
		return KindUnknown
	}
}

// Label 生成 "kind: msg" 形态的日志字段值；空类别原样返回。
func (k ErrorKind) Label(msg string) string {
	if k == "" {
		return msg
	}
	return string(k) + ": " + msg
}
