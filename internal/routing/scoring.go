// scoring.go 是 M2.4 打分路由 v1（docs/05 2.4）：按 健康度·延迟·剩余配额
// 加权排序候选，把劣化的 provider 沉到候选列表尾部。与 M2.2/M2.3 的分工：
// 隔离是硬边界（跳过），打分是软偏好（排序）；先 rank 后 skip，被隔离或
// 配额耗尽的 key 根本不进尝试。内存态不持久化，重启即回到注册序冷启动。
package routing

import (
	"sort"
	"sync"
	"time"

	"github.com/swsgbl/omnifusion/internal/provider"
)

// DefaultScoringWeights 是 v1 加权：健康度为主，延迟次之，配额余量兜底。
var DefaultScoringWeights = ScoringWeights{Health: 0.5, Latency: 0.3, Quota: 0.2}

// ScoringWeights 是三路信号的线性组合系数（无需归一，按相对大小生效）。
type ScoringWeights struct {
	Health  float64 // EWMA 成功率
	Latency float64 // 延迟归一项（0ms→1，1s→0.5，3s→0.25）
	Quota   float64 // 四窗口最紧的剩余配额比例
}

// ScoreSignals 暴露打分的原始三路信号（ofd status / 日志观测用）。
type ScoreSignals struct {
	Health  float64
	Latency float64
	Quota   float64
}

// EWMA 新息权重：延迟抖动大取 0.3 快跟，成功率取 0.2 稳一点。
const (
	latencyAlpha = 0.3
	successAlpha = 0.2
)

// Scorer 汇总每 provider 的真实尝试观测（耗时 + 成败），产出排序分。
// 零值不可用，经 NewScorer 构造；未观测过的 provider 取乐观默认
// （延迟 0、成功率 1），新 key 先被探索再被排序。
type Scorer struct {
	mu     sync.Mutex
	lat    map[string]float64 // EWMA 毫秒（Translate → 响应/首 chunk 落地）
	succ   map[string]float64 // EWMA 成功率 [0,1]
	lastOK map[string]time.Time
	now    func() time.Time // 时钟注入点（lkgp 测试用）
	Weight ScoringWeights
}

// NewScorer 装配默认权重的打分器。
func NewScorer() *Scorer {
	return &Scorer{
		lat:    map[string]float64{},
		succ:   map[string]float64{},
		lastOK: map[string]time.Time{},
		now:    time.Now,
		Weight: DefaultScoringWeights,
	}
}

// Observe 提交一次真实尝试的耗时与结果。只记 tryOne 级别的尝试；
// 配额跳过、不支持流等非上游结果不观测。nil 接收者安全。
// 首次观测直接落地（不从 0/1 混合起步，避免冷启动失真）。
func (s *Scorer) Observe(name string, d time.Duration, success bool) {
	if s == nil {
		return
	}
	ms := float64(d.Microseconds()) / 1000.0
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, seen := s.lat[name]; seen {
		s.lat[name] = (1-latencyAlpha)*s.lat[name] + latencyAlpha*ms
	} else {
		s.lat[name] = ms
	}
	if _, seen := s.succ[name]; seen {
		s.succ[name] = (1-successAlpha)*s.succ[name] + successAlpha*boolTo01(success)
	} else {
		s.succ[name] = boolTo01(success)
	}
	if success {
		s.lastOK[name] = s.now()
	}
}

// ObserveFailure 只记一次健康降分，不产生延迟观测（docs/04 §4.4：
// stream_broken 的 HealthPenalty——首 chunk 已落地的流中途断裂，
// 延迟口径仍是"到首 chunk"，断裂不更新延迟与 lastOK）。nil 接收者安全。
func (s *Scorer) ObserveFailure(name string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, seen := s.succ[name]; seen {
		s.succ[name] = (1 - successAlpha) * s.succ[name] // + successAlpha*0
	} else {
		s.succ[name] = 0
	}
}

// LastSuccessAt 返回该 provider 最近一次成功的时间；从未成功过时
// 第二返回值为 false。lkgp 策略（M2.5）消费。
func (s *Scorer) LastSuccessAt(name string) (time.Time, bool) {
	if s == nil {
		return time.Time{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ts, ok := s.lastOK[name]
	return ts, ok
}

// Snapshot 返回该 provider 当前的延迟 EWMA（毫秒）与成功率 EWMA；
// 未观测过返回 (0, 1)。
func (s *Scorer) Snapshot(name string) (latencyMS, successRate float64) {
	if s == nil {
		return 0, 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	latencyMS, seenLat := s.lat[name]
	if !seenLat {
		latencyMS = 0
	}
	successRate, seenSucc := s.succ[name]
	if !seenSucc {
		successRate = 1
	}
	return latencyMS, successRate
}

// Score 计算单 provider 的排序分（越高越优先）并返回三路信号。
func (s *Scorer) Score(name string, q *QuotaTracker) (float64, ScoreSignals) {
	if s == nil {
		return 1, ScoreSignals{Health: 1, Latency: 1, Quota: 1}
	}
	w := s.Weight
	if w.Health+w.Latency+w.Quota <= 0 {
		w = DefaultScoringWeights
	}
	ms, succ := s.Snapshot(name)
	signals := ScoreSignals{
		Health:  succ,
		Latency: 1 / (1 + ms/1000),
		Quota:   1,
	}
	if q != nil {
		signals.Quota = q.Headroom(name)
	}
	return w.Health*signals.Health + w.Latency*signals.Latency + w.Quota*signals.Quota, signals
}

// rank 返回按分数降序的候选副本（稳定排序：同分保持注册序）；
// Scorer 未启用时原样返回注册序（M1 行为）。
func (r *Router) rank() []provider.Provider {
	if r.Scoring == nil {
		return r.Providers
	}
	sorted := append([]provider.Provider(nil), r.Providers...)
	sort.SliceStable(sorted, func(i, j int) bool {
		si, _ := r.Scoring.Score(sorted[i].Name(), r.Quota)
		sj, _ := r.Scoring.Score(sorted[j].Name(), r.Quota)
		return si > sj
	})
	return sorted
}

func boolTo01(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
