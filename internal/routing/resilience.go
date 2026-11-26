// resilience.go 是 三层隔离状态机：
//
//	Connection 冷却 provider 级；rate_limit 30s / auth_invalid 30min（策略表）
//	Model 锁定 provider+model 级；quota_exhausted 至重置点（reset-aware v1 固定 1h）
//	Breaker 熔断 provider 级错误率熔断（breaker.go）
//
// 冷却与锁定持久化到 SQLite cooldowns 表，重启恢复（FreeRide 教训）；
// 熔断退避 30s 级自愈，仅内存。路由层在尝试前问 Block/LockedModel，
// 尝试后回报 ApplyFailure/ApplySuccess。锁纪律：这两个入口按请求粒度
// 加锁（每请求一次，非 per-chunk 热路径，不违反 item1）。
package routing

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/store"
)

// QuotaLockoutDuration 是 quota_exhausted 的默认 Model 锁定时长。
// reset-aware v1：暂不解析上游 reset 头/响应体，先锁 1h； 配额
// 窗口接入后按 provider 的真实重置点修正。
const QuotaLockoutDuration = time.Hour

// Isolation 汇聚三层隔离状态。零值不可用，经 NewIsolation 构造。
type Isolation struct {
	mu        sync.Mutex
	cooldowns map[string]time.Time // provider → until（Connection 层）
	lockouts  map[string]time.Time // provider\x00model → until（Model 层）
	breakers  map[string]*breaker
	st        *store.Store // nil = 仅内存（测试/降级）
	log       *slog.Logger
	now       func() time.Time // 时钟注入点（测试用）
}

// NewIsolation 装配隔离状态机；st 非 nil 时从 SQLite 恢复活跃条目。
func NewIsolation(st *store.Store, log *slog.Logger) (*Isolation, error) {
	iso := &Isolation{
		cooldowns: map[string]time.Time{},
		lockouts:  map[string]time.Time{},
		breakers:  map[string]*breaker{},
		st:        st,
		log:       log,
		now:       time.Now,
	}
	if st != nil {
		if err := iso.restore(st); err != nil {
			return nil, err
		}
	}
	return iso, nil
}

func (iso *Isolation) restore(st *store.Store) error {
	rows, err := st.LoadCooldowns(time.Now())
	if err != nil {
		return fmt.Errorf("isolation restore: %w", err)
	}
	for _, c := range rows {
		switch c.ScopeType {
		case "connection":
			iso.cooldowns[c.Provider] = c.Until
		case "model":
			iso.lockouts[c.Provider+"\x00"+c.Model] = c.Until
		}
	}
	if iso.log != nil && (len(iso.cooldowns) > 0 || len(iso.lockouts) > 0) {
		iso.log.Info("isolation state restored",
			"cooldowns", len(iso.cooldowns), "lockouts", len(iso.lockouts))
	}
	return nil
}

// Block 报告 provider 是否被隔离（冷却中/熔断开）及原因。
func (iso *Isolation) Block(provider string) (bool, string) {
	iso.mu.Lock() // blocked() 可能推进 open→half-open，须写锁
	defer iso.mu.Unlock()
	now := iso.now()
	if until, ok := iso.cooldowns[provider]; ok && now.Before(until) {
		return true, "cooldown until " + until.UTC().Format(time.RFC3339)
	}
	if b := iso.breakers[provider]; b != nil {
		if blocked, left := b.blocked(now); blocked {
			return true, fmt.Sprintf("breaker open (%s left)", left.Round(time.Second))
		}
	}
	return false, ""
}

// LockedModel 报告 provider 的指定模型是否被配额锁定及原因。
func (iso *Isolation) LockedModel(provider, model string) (bool, string) {
	iso.mu.Lock()
	defer iso.mu.Unlock()
	if until, ok := iso.lockouts[provider+"\x00"+model]; ok && iso.now().Before(until) {
		return true, "model locked until " + until.UTC().Format(time.RFC3339)
	}
	return false, ""
}

// ApplyFailure 按策略表（PolicyFor）落地一次失败。
// kind 为空/unknown 时不动作——那不是上游的病（跳过记录、请求错误）。
func (iso *Isolation) ApplyFailure(provider, model string, kind ErrorKind) {
	p := PolicyFor(kind)
	now := iso.now()
	iso.mu.Lock()
	defer iso.mu.Unlock()
	if p.Cooldown > 0 {
		iso.setCooldown(provider, now.Add(p.Cooldown), string(kind))
	}
	if p.LockoutModel && model != "" {
		iso.setLockout(provider, model, now.Add(lockoutDuration(kind)), string(kind))
	}
	if breakerEligible(kind) {
		b := iso.breaker(provider)
		b.onFailure(now)
	}
}

// LockModel 以模型级语义落地一次失败：只锁定 (provider, model)，不冷却
// 连接、不计熔断——用于"整家没病，只是这个模型不行"的场景（免密网关
// 的付费模型 401、聚合面的单模型限流；判据见 modelScopedFailure）。
func (iso *Isolation) LockModel(provider, model string, d time.Duration, kind ErrorKind) {
	if model == "" {
		return
	}
	now := iso.now()
	iso.mu.Lock()
	defer iso.mu.Unlock()
	iso.setLockout(provider, model, now.Add(d), string(kind))
}

// ApplyDialFailure 记一次"连接根本没建立起来"的失败（dial 超时/拒绝/
// DNS 失败）。它与"连上了但对端慢"不同：主机不可达（DNS 污染、本地
// 服务没启动）几乎不会自愈，而常规熔断要凑满窗口阈值——用户要连续
// 多个请求各挂满一次拨号预算才等得到熔断。连续两次即熔断，此后沿用
// 指数退避；成功即清零。
func (iso *Isolation) ApplyDialFailure(provider string) {
	now := iso.now()
	iso.mu.Lock()
	defer iso.mu.Unlock()
	iso.breaker(provider).onDialFailure(now)
}

// breaker 返回 provider 的熔断器（调用方持锁；惰性创建）。
func (iso *Isolation) breaker(provider string) *breaker {
	b := iso.breakers[provider]
	if b == nil {
		b = &breaker{}
		iso.breakers[provider] = b
	}
	return b
}

// lockoutDuration 是 LockoutModel 类错误的 Model 锁定时长：配额类锁到
// 重置点（reset-aware v1 固定 1h），模型下线类短得多（改配置可解）。
func lockoutDuration(kind ErrorKind) time.Duration {
	if kind == KindModelGone {
		return ModelGoneLockout
	}
	return QuotaLockoutDuration
}

// ApplySuccess 回报一次成功：熔断探测通过即闭合；冷却不动（到点自然恢复）。
func (iso *Isolation) ApplySuccess(provider string) {
	iso.mu.Lock()
	defer iso.mu.Unlock()
	if b := iso.breakers[provider]; b != nil {
		b.onSuccess()
	}
}

// Clear 人工清除一个 provider 的全部隔离态（运维动作，MCP
// route 工具经控制 API 调用）：Connection 冷却、Model 锁定与熔断
// 计数（内存）+ SQLite cooldowns 行（重启后也不再恢复）。返回清除
// 的持久化条数；存储失败只降级为内存清除（清除是加速恢复，不因
// 存储故障拒绝）。
func (iso *Isolation) Clear(provider string) int {
	iso.mu.Lock()
	delete(iso.cooldowns, provider)
	for k := range iso.lockouts {
		if strings.HasPrefix(k, provider+"\x00") {
			delete(iso.lockouts, k)
		}
	}
	if b := iso.breakers[provider]; b != nil {
		b.onSuccess() // 熔断计数归零（复用成功探测闭合）
	}
	iso.mu.Unlock()

	if iso.st == nil {
		return 0
	}
	n, err := iso.st.ClearCooldowns(provider)
	if err != nil && iso.log != nil {
		iso.log.Warn("clear cooldowns failed", "provider", provider, "err", err)
	}
	return int(n)
}

func (iso *Isolation) setCooldown(provider string, until time.Time, reason string) {
	if prev, ok := iso.cooldowns[provider]; !ok || until.After(prev) {
		iso.cooldowns[provider] = until
		iso.persist(store.Cooldown{
			ScopeType: "connection", Provider: provider, Until: until, Reason: reason,
		})
	}
}

func (iso *Isolation) setLockout(provider, model string, until time.Time, reason string) {
	key := provider + "\x00" + model
	if prev, ok := iso.lockouts[key]; !ok || until.After(prev) {
		iso.lockouts[key] = until
		iso.persist(store.Cooldown{
			ScopeType: "model", Provider: provider, Model: model, Until: until, Reason: reason,
		})
	}
}

// persist 尽力持久化：失败只记日志，隔离逻辑绝不因存储故障中断分发。
func (iso *Isolation) persist(c store.Cooldown) {
	if iso.st == nil {
		return
	}
	if err := iso.st.UpsertCooldown(c); err != nil && iso.log != nil {
		iso.log.Warn("persist cooldown failed", "provider", c.Provider, "model", c.Model, "err", err)
	}
}

// breakerEligible 报告该类错误是否计入熔断窗口：只有"上游病了"
// （5xx/网络/断流）才算；限流/配额/鉴权/请求错误是容量与凭据语义。
func breakerEligible(kind ErrorKind) bool {
	switch kind {
	case KindUpstream5xx, KindNetOrTimeout, KindStreamBroken:
		return true
	}
	return false
}

// skipIfBlocked 检查 provider 级阻断：配额窗口将爆与三层隔离
// （冷却/熔断）；命中时产出 skip 记录（Kind 留空：不是上游错误，
// 不参与分类惩罚；reason 结构化进 SkipReason 供决策回放）。
func (r *Router) skipIfBlocked(p provider.Provider) (Attempt, bool) {
	if r.Quota != nil {
		if blocked, reason := r.Quota.Blocked(p.Name()); blocked {
			if r.Log != nil {
				r.Log.Warn("provider quota exhausted, skip", "provider", p.Name(), "reason", reason)
			}
			return Attempt{
				Provider:   p.Name(),
				Err:        fmt.Errorf("routing: skipped (%s)", reason),
				SkipReason: reason,
			}, true
		}
	}
	if r.Isolation == nil {
		return Attempt{}, false
	}
	blocked, reason := r.Isolation.Block(p.Name())
	if !blocked {
		return Attempt{}, false
	}
	if r.Log != nil {
		r.Log.Warn("provider isolated, skip", "provider", p.Name(), "reason", reason)
	}
	return Attempt{
		Provider:   p.Name(),
		Err:        fmt.Errorf("routing: skipped (%s)", reason),
		SkipReason: reason,
	}, true
}

// applyIsolation 把一次尝试的结果反馈给隔离状态机（成功→探测闭合，
// 失败→按策略冷却/锁定/计数）。p 用于判定模型级语义（免密/聚合面）
// 与建连失败。
func (r *Router) applyIsolation(p provider.Provider, att Attempt) {
	if r.Isolation == nil {
		return
	}
	name := p.Name()
	if att.Err == nil {
		r.Isolation.ApplySuccess(name)
		return
	}
	if att.Kind == KindNetOrTimeout && isDialError(att.Err) {
		r.Isolation.ApplyDialFailure(name)
		return
	}
	if d, kind, ok := modelScopedFailure(p, att); ok {
		r.Isolation.LockModel(name, att.Model, d, kind)
		return
	}
	r.Isolation.ApplyFailure(name, att.Model, att.Kind)
}

// modelScopedFailure 判定一次失败是否只该落到模型级——连接本身没病，
// 换个模型同一网关照样能用，冷却整家只会把好模型一起拖死：
//   - 免密实例（provider.Anonymous）的 401/403：免费池匿名可调，付费
//     模型要求登录（Kilo PAID_MODEL_AUTH_REQUIRED）。2026-11-25 用户
//     实撞：静态清单里一个转收费的旧模型回 401，整个免密免费池被
//     隔离 30 分钟（日志里该家"isolated, skip" 15 次）；
//   - 聚合面（provider.Aggregator）的限流：429 来自该模型背后的上游
//     （OpenRouter "Provider returned error"），同家别的模型此刻可用。
//
// 返回锁定时长与错误类别；ok=false 走常规策略表。
func modelScopedFailure(p provider.Provider, att Attempt) (time.Duration, ErrorKind, bool) {
	switch att.Kind {
	case KindAuthInvalid:
		if a, ok := p.(provider.Anonymous); ok && a.Keyless() {
			return AuthInvalidCooldown, att.Kind, true
		}
	case KindRateLimit:
		if a, ok := p.(provider.Aggregator); ok && a.IsAggregator() {
			return RateLimitCooldown, att.Kind, true
		}
	}
	return 0, "", false
}

// isDialError 报告网络错误是否发生在建连阶段（TCP dial 失败：超时、
// 拒绝、DNS 解析失败）。只认 typed error（*net.OpError 的 Op 字段），
// 不做字符串匹配。
func isDialError(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}
