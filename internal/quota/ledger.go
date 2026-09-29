// Package quota 是权益账本（Entitlement Ledger，升级蓝图 Phase 3 /
// 施工方案 §四）：把"免费额度 / 配额"从 catalog 静态声明字段升级为
// 带来源、时效、置信度的可验证事实对象。
//
// 五态模型（蓝图 §六："UNKNOWN 不等于 FREE"）：
//
//	UNKNOWN       —— 没有证据（新接入、从未观测）
//	VERIFIED_FREE —— 有证据支持免费层可用
//	VERIFIED_PAID —— 有证据支持需要付费（free 层已不可用）
//	EXPIRED       —— 曾有证据，但已超出时效窗口（自动从 VERIFIED_* 降级）
//	DISABLED      —— 人工/策略显式停用
//
// 现有基建的关系（Phase 0 审计结论）：
//   - routing.QuotaTracker：运行时滑动窗口用量（本地观测事实）——
//     作为 ObservationSource 之一喂进账本，不是账本本身；
//   - registry.RateLimitsDecl：静态声明的窗口大小（容量事实）——
//     Source=StaticCatalog 的 cap 字段来源；
//   - routing.Catalog 的 pricing（PriceResolver）：静态定价声明——
//     Source=StaticCatalog 的 state 字段来源。
//
// 账本是它们的汇合点：同一 (provider, model) 上的多源证据按优先级
// 合并、按时效降级，产出路由可信赖的唯一现状。
package quota

import (
	"sort"
	"sync"
	"time"
)

// EntitlementState 是权益五态。
type EntitlementState string

const (
	StateUnknown      EntitlementState = "UNKNOWN"
	StateVerifiedFree EntitlementState = "VERIFIED_FREE"
	StateVerifiedPaid EntitlementState = "VERIFIED_PAID"
	StateExpired      EntitlementState = "EXPIRED"
	StateDisabled     EntitlementState = "DISABLED"
)

// Source 是证据来源（蓝图：静态 catalog、官方声明、主动探测、运行时
// 观测必须区分——来源决定可信度与时效语义）。
type Source string

const (
	SourceStaticCatalog Source = "static_catalog" // registry YAML / 签名 feed 声明
	SourceOfficialDoc   Source = "official_doc"   // 厂商官方文档/公告链接
	SourceActiveProbe   Source = "active_probe"   // ofd provider verify 主动探测
	SourceRuntime429    Source = "runtime_429"    // 运行时 429/配额耗尽观测
	SourceRuntimeUsage  Source = "runtime_usage"  // 运行时成功用量观测
	SourceManual        Source = "manual"         // 人工覆盖（最高优先）
)

// sourcePriority 是同 (provider, model) 多源冲突时的合并优先级。
// 人工 > 运行时观测（反映"现在"）> 主动探测 > 官方文档 > 静态声明。
var sourcePriority = map[Source]int{
	SourceManual:        100,
	SourceRuntime429:    80,
	SourceRuntimeUsage:  70,
	SourceActiveProbe:   50,
	SourceOfficialDoc:   40,
	SourceStaticCatalog: 10,
}

// QuotaWindow 是单窗口的容量与余量（两者都允许 unknown——蓝图：
// "quota UNKNOWN 不允许伪装成剩余很多或剩余 0"）。
type QuotaWindow struct {
	RPM int   `json:"rpm,omitempty"` // requests/min cap；0=未知或不限
	TPM int64 `json:"tpm,omitempty"` // tokens/min cap
	RPD int   `json:"rpd,omitempty"` // requests/day cap
	TPD int64 `json:"tpd,omitempty"` // tokens/day cap
	// Remaining 声明观测时的余量比例 [0,1]；-1 = 未知。静态声明无
	// 余量概念（恒 -1）；运行时观测才填。
	Remaining float64 `json:"remaining,omitempty"`
}

// Entitlement 是账本的单条事实：某 (provider, model) 在某时刻的
// 权益现状与证据链。明文密钥绝不入账（keyRef 只存引用标识）。
type Entitlement struct {
	Provider string           `json:"provider"`
	Model    string           `json:"model"` // 空 = provider 级事实
	State    EntitlementState `json:"state"`
	// Window 是容量/余量声明（各字段 0 = unknown，不猜）。
	Window QuotaWindow `json:"window,omitempty"`
	// Source 是本条结论的证据来源。
	Source Source `json:"source"`
	// EvidenceID 指向支撑结论的证据记录（蓝图：任何 free 结论都带
	// evidence id；无证据的 free 不允许写成 VERIFIED_FREE）。
	EvidenceID string `json:"evidence_id,omitempty"`
	// TermsURL 是厂商条款链接（"可调用"≠"允许任意聚合使用"）。
	TermsURL string `json:"terms_url,omitempty"`
	// ObservedAt 是证据产生时刻；ValidUntil 是时效边界（零值=永久，
	// 仅 Manual/StaticCatalog 可永久）。超出 ValidUntil 自动降级 EXPIRED。
	ObservedAt time.Time `json:"observed_at"`
	ValidUntil time.Time `json:"valid_until,omitempty"`
	// Confidence 是置信度 [0,1]：StaticCatalog=0.5、官方文档=0.7、
	// 主动探测=0.85、运行时观测=0.9、人工=1.0（可覆写）。
	Confidence float64 `json:"confidence"`
	// CatalogVersion 记录证据对应的目录版本（可追溯）。
	CatalogVersion string `json:"catalog_version,omitempty"`
}

// Ledger 是权益账本：并发安全的 (provider, model) → Entitlement 存储，
// 支持多源记录合并与时效降级。零值可用（纯内存形态）。装配
// SetPersister 后 Record 自动落 SQLite（重启恢复经 LoadFrom）。
type Ledger struct {
	mu   sync.RWMutex
	ents map[string]Entitlement
	// persist 是可选持久化钩子（store.SaveEntitlement；nil=纯内存）。
	persist func(Entitlement) error
}

// Persister 是账本的持久化接口（internal/store 实现；quota 包不依赖
// store 以保持依赖叶子——蓝图分层纪律）。
type Persister interface {
	SaveEntitlement(e Entitlement) error
}

// SetPersister 装配持久化钩子（cmd/ofd 启动期调用）。已装配后每次
// Record 合并生效都自动落库；落库失败不阻断内存路径（账本语义优先，
// 持久化是尽力而为——下次合并会重写该键）。
func (l *Ledger) SetPersister(p Persister) {
	if p == nil {
		return
	}
	l.persist = func(e Entitlement) error { return p.SaveEntitlement(e) }
}

// LoadFrom 从持久化层恢复快照（启动期调用；按合并规则逐条 Record，
// 与静态种子共存——运行时证据优先级更高自然覆盖）。
func (l *Ledger) LoadFrom(rows []Entitlement) {
	for _, e := range rows {
		l.Record(e)
	}
}

// NewLedger 构造空账本。
func NewLedger() *Ledger {
	return &Ledger{ents: make(map[string]Entitlement)}
}

func key(provider, model string) string { return provider + "/" + model }

// Record 写入一条证据并按合并规则更新现状。规则：
//  1. 新证据 Source 优先级 > 存量 → 覆盖；
//  2. 同级 → ObservedAt 更新者胜（新鲜度）；
//  3. State=DISABLED 只能被 Manual 覆盖（策略性停用不被观测噪声翻案）。
//
// 装配了持久化钩子时，合并生效的写入同步落库（尽力而为）。
func (l *Ledger) Record(e Entitlement) {
	if e.Provider == "" {
		return
	}
	if e.Confidence == 0 {
		e.Confidence = defaultConfidence(e.Source)
	}
	k := key(e.Provider, e.Model)
	l.mu.Lock()
	old, exists := l.ents[k]
	if !exists || shouldReplace(old, e) {
		l.ents[k] = e
	} else {
		l.mu.Unlock()
		return // 未合并生效：不落库（避免用低优先级证据覆写持久层）
	}
	persist := l.persist
	l.mu.Unlock()
	if persist != nil {
		_ = persist(e) // 尽力而为：失败不阻断请求路径
	}
}

func shouldReplace(old, neu Entitlement) bool {
	if old.State == StateDisabled && neu.Source != SourceManual {
		return false
	}
	// 存量证据已过时效（蓝图：过期数据自动降级，不能静默继续当事实）
	// → 任何新鲜证据（哪怕来源优先级低）都胜出：一小时前的 429 不该
	// 压住刚刚成功的用量观测。
	now := time.Now()
	oldStale := !old.ValidUntil.IsZero() && now.After(old.ValidUntil)
	neuStale := !neu.ValidUntil.IsZero() && now.After(neu.ValidUntil)
	if oldStale != neuStale {
		return neuStale == false // 新鲜者胜（旧过期/新未过期）
	}
	if oldStale && neuStale {
		return !neu.ObservedAt.Before(old.ObservedAt) // 双过期：仍取更新的观测时刻
	}
	po, pn := sourcePriority[old.Source], sourcePriority[neu.Source]
	if pn != po {
		return pn > po
	}
	return !neu.ObservedAt.Before(old.ObservedAt) // 同级新鲜者胜
}

func defaultConfidence(s Source) float64 {
	switch s {
	case SourceManual:
		return 1.0
	case SourceRuntime429, SourceRuntimeUsage:
		return 0.9
	case SourceActiveProbe:
		return 0.85
	case SourceOfficialDoc:
		return 0.7
	default:
		return 0.5
	}
}

// Get 取某 (provider, model) 的现状；过期自动降级 EXPIRED（降级结果
// 写回，不留旧事实冒充有效——蓝图："过期数据自动降级，不能静默继续
// 当事实"）。无记录返回 UNKNOWN 零值。
func (l *Ledger) Get(provider, model string) Entitlement {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.ents[key(provider, model)]
	if !ok {
		return Entitlement{Provider: provider, Model: model, State: StateUnknown,
			Window: QuotaWindow{Remaining: -1}}
	}
	if !e.ValidUntil.IsZero() && time.Now().After(e.ValidUntil) &&
		e.State != StateDisabled && e.State != StateExpired {
		e.State = StateExpired
		e.Window.Remaining = -1
		l.ents[key(provider, model)] = e
	}
	return e
}

// RecordQuotaExhausted 是运行时 429 的便捷入口：从"配额真的爆了"
// 的直接证据反推 free 层可用但已耗尽（State 仍 VERIFIED_FREE——
// 耗尽是窗口语义不是权益语义；Window.Remaining 归 0，ValidUntil 给
// 一个保守的观测时效）。
func (l *Ledger) RecordQuotaExhausted(provider, model string, observed time.Time) {
	l.Record(Entitlement{
		Provider: provider, Model: model,
		State:      StateVerifiedFree,
		Source:     SourceRuntime429,
		Window:     QuotaWindow{Remaining: 0},
		ObservedAt: observed,
		ValidUntil: observed.Add(1 * time.Hour),
		EvidenceID: "429:" + observed.UTC().Format(time.RFC3339),
	})
}

// RecordPaidObservation 是运行时付费证据（如 402/insufficient_quota）：
// free 层已不可用，State 升 VERIFIED_PAID。
func (l *Ledger) RecordPaidObservation(provider, model string, observed time.Time) {
	l.Record(Entitlement{
		Provider: provider, Model: model,
		State:      StateVerifiedPaid,
		Source:     SourceRuntime429,
		ObservedAt: observed,
		ValidUntil: observed.Add(24 * time.Hour),
		EvidenceID: "paid:" + observed.UTC().Format(time.RFC3339),
	})
}

// Disable 人工停用（仅 Manual 可翻案）。
func (l *Ledger) Disable(provider, model, reason string) {
	l.Record(Entitlement{
		Provider: provider, Model: model,
		State: StateDisabled, Source: SourceManual,
		EvidenceID: "manual:" + reason,
	})
}

// Snapshot 导出全量现状（dashboard / 审计面）。
func (l *Ledger) Snapshot() []Entitlement {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Entitlement, 0, len(l.ents))
	for _, e := range l.ents {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Model < out[j].Model
	})
	return out
}
