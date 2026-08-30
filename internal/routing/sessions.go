// sessions.go 实现 sticky session（ 2.7）：同一
// X-Session-Id 的请求粘住最近成功的 provider，30 分钟滑动过期（每次
// 成功续期）。绑定的 provider 被隔离/配额阻断时自动让位——本请求走
// 正常候选序，成功后重绑到新赢家（"同会话粘住同 provider 直至冷却"）。
package routing

import (
	"sync"
	"time"

	"github.com/swsgbl/omnifusion/internal/provider"
)

// HeaderSession 是声明会话亲和的请求头名。
const HeaderSession = "X-Session-Id"

// WithSession 声明本次分发的会话亲和（server 层从 HeaderSession 提取）。
func WithSession(id string) DispatchOption {
	return func(c *dispatchConfig) { c.sessionID = id }
}

// sessionTTL 是绑定的滑动有效期。
const sessionTTL = 30 * time.Minute

// sessionSweepThreshold 触发过期清扫的规模（个人网关量级下的兜底，
// 防长尾死会话撑大 map）。
const sessionSweepThreshold = 1024

type sessionBinding struct {
	provider string
	expires  time.Time
}

// SessionTracker 记录 会话→provider 绑定；零值不可用，经
// NewSessionTracker 构造。
type SessionTracker struct {
	mu   sync.Mutex
	bind map[string]sessionBinding
	now  func() time.Time // 时钟注入点（测试用）
}

// NewSessionTracker 装配会话追踪器。
func NewSessionTracker() *SessionTracker {
	return &SessionTracker{bind: map[string]sessionBinding{}, now: time.Now}
}

// Bound 返回该会话当前绑定的 provider；未绑定或已过期时 ok=false
// （过期即忘，惰性清理）。
func (s *SessionTracker) Bound(id string) (string, bool) {
	if s == nil || id == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bind[id]
	if !ok || !b.expires.After(s.now()) {
		return "", false
	}
	return b.provider, true
}

// Bind 记录/续期一条绑定（成功分发后调用）。
func (s *SessionTracker) Bind(id, provider string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bind) >= sessionSweepThreshold {
		s.sweepLocked()
	}
	s.bind[id] = sessionBinding{provider: provider, expires: s.now().Add(sessionTTL)}
}

// sweepLocked 清除已过期绑定（调用方持锁）。
func (s *SessionTracker) sweepLocked() {
	now := s.now()
	for id, b := range s.bind {
		if !b.expires.After(now) {
			delete(s.bind, id)
		}
	}
}

// applySticky 把会话绑定的 provider 提到候选首位（其余保持策略序）；
// 绑定不存在或 provider 已离场时原样返回。绑定者即便已被隔离/配额
// 阻断也照样提升——让位由主循环的 skipIfBlocked 统一执行并留 skip
// 记录（单一执法点，trace 可见），成功后由 bindSession 自然重绑。
func (r *Router) applySticky(cands []provider.Provider, sessionID string) []provider.Provider {
	if r.Sessions == nil || sessionID == "" || len(cands) <= 1 {
		return cands
	}
	bound, ok := r.Sessions.Bound(sessionID)
	if !ok {
		return cands
	}
	for i, p := range cands {
		if p.Name() != bound {
			continue
		}
		out := make([]provider.Provider, 0, len(cands))
		out = append(out, p)
		out = append(out, cands[:i]...)
		return append(out, cands[i+1:]...)
	}
	return cands
}

// bindSession 在分发成功后记录绑定（id 为空或未启用时是 no-op）。
func (r *Router) bindSession(sessionID, provider string) {
	if r.Sessions != nil && sessionID != "" {
		r.Sessions.Bind(sessionID, provider)
	}
}
