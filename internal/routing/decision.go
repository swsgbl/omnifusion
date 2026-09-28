// decision.go 是 Router 2.0 的可回放路由决策证据（升级蓝图 Phase 2
// 首个切片，2026-09-29 施工方案 §四）：
//
//	RouteDecision { RequestID, CandidateSet, Chosen..., ReasonCodes[],
//	                HardFilterReasons{}, PolicySnapshotID, ... }
//
// 现有 Dispatch 已经掌握这些事实（候选集、逐候选 Attempt.Kind、
// skipIfBlocked 的结构化 SkipReason），但没有一个可序列化的回放对象。
// 本切片把它显式化：Decide() 包一层 Dispatch，产出决策证据；
// 既有 Dispatch/DispatchStream 行为零变更（仅 Attempt 增加了
// SkipReason 结构化字段，默认零值不影响旧路径）。
package routing

import (
	"context"
	"strings"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// ReasonCode 是路由决策的原因码（蓝图 §五：用户看到的是"为什么选它"，
// 不是黑盒分数）。第一阶段先落选择面与跳过面的事实码；评分面
// （score breakdown）在 Scorer 接入样本量/时间窗语义后并入。
type ReasonCode string

const (
	// 选择面
	ReasonFirstSuccess    ReasonCode = "FIRST_SUCCESS"   // 候选序首位一次成功
	ReasonFailoverChosen  ReasonCode = "FAILOVER_CHOSEN" // 前序失败后此候选成功
	ReasonAllExhausted    ReasonCode = "ALL_CANDIDATES_EXHAUSTED"
	ReasonContextCanceled ReasonCode = "CONTEXT_CANCELED"

	// 候选集构建面（候选为什么进序列；由调用方按指令语义传入）
	ReasonQualityAuto  ReasonCode = "QUALITY_AUTO"  // @quality 自动选强生成候选
	ReasonCheapAuto    ReasonCode = "CHEAP_AUTO"    // 裸 @cheap 三档排序
	ReasonSmartPlan    ReasonCode = "SMART_ML_PLAN" // @smart ML 计划
	ReasonComboMembers ReasonCode = "COMBO_MEMBERS" // @combo 组合成员
	ReasonDirectModel  ReasonCode = "DIRECT_MODEL"  // 客户端显式模型

	// 硬过滤/跳过面（候选在序列里被跳过；由 SkipReason 归类）
	ReasonCircuitOpen    ReasonCode = "CIRCUIT_OPEN"    // provider 熔断中
	ReasonCooldownActive ReasonCode = "COOLDOWN_ACTIVE" // key/连接冷却中
	ReasonModelLocked    ReasonCode = "MODEL_LOCKED"    // 模型额度锁定
	ReasonQuotaWindow    ReasonCode = "QUOTA_WINDOW"    // 配额滑动窗口将爆
)

// DecisionCandidate 是决策证据里的单个候选：选择序位置 + 尝试结果
// 分类（未尝试=被跳过，附跳过码）。
type DecisionCandidate struct {
	Order      int        `json:"order"` // 尝试序列位置（0 起）
	Provider   string     `json:"provider"`
	Model      string     `json:"model,omitempty"`   // 别名重写后的实际模型
	Kind       string     `json:"kind,omitempty"`    // Attempt.Kind（成功为空）
	Skipped    bool       `json:"skipped,omitempty"` // 序列内被跳过（隔离/配额态）
	SkipReason ReasonCode `json:"skip_reason,omitempty"`
}

// RouteDecision 是一次分发请求的完整决策证据，可 JSON 序列化落
// audit/replay（蓝图 §五：同任务集路由质量可回放）。
type RouteDecision struct {
	RequestID       string              `json:"request_id,omitempty"`     // 调用方注入（server 层 request_id）
	Model           string              `json:"model"`                    // 客户端请求的模型别名
	ResolvedModel   string              `json:"resolved_model,omitempty"` // 选中候选的实际模型
	ChosenProvider  string              `json:"chosen_provider,omitempty"`
	CandidateSource ReasonCode          `json:"candidate_source,omitempty"` // 候选集构建方式
	Candidates      []DecisionCandidate `json:"candidates"`
	ReasonCodes     []ReasonCode        `json:"reason_codes"`
	AttemptCount    int                 `json:"attempt_count"`
	Success         bool                `json:"success"`
	DurationMS      int64               `json:"duration_ms"`
	// PolicySnapshotID 留待策略快照接入（当前恒空）。
	PolicySnapshotID string `json:"policy_snapshot_id,omitempty"`
}

// Decide 执行 Dispatch 并把过程折叠为 RouteDecision。候选集构建方式
// （candidateSource）由调用方按指令语义传入——Dispatch 内部的
// candidatesFor 分流不外泄，调用方在构造 opts 时知道自己给的是什么指令。
// 零侵入：不改 Dispatch 签名；后续切片把决策证据提升为 Dispatch 的
// 可选返回后，这里退化为薄委托。
func (r *Router) Decide(ctx context.Context, req *schema.UnifiedRequest, candidateSource ReasonCode, opts ...DispatchOption) (*schema.Response, *RouteDecision, error) {
	start := time.Now()
	resp, attempts, err := r.Dispatch(ctx, req, opts...)

	d := &RouteDecision{
		RequestID:       req.User, // 暂复用 User 作关联键；server 注入 request_id 后替换
		Model:           req.Model,
		CandidateSource: candidateSource,
		AttemptCount:    len(attempts),
		DurationMS:      time.Since(start).Milliseconds(),
	}

	for i, att := range attempts {
		dc := DecisionCandidate{
			Order:    i,
			Provider: att.Provider,
			Model:    att.Model,
			Kind:     string(att.Kind),
		}
		if att.SkipReason != "" {
			dc.Skipped = true
			dc.SkipReason = classifySkipReason(att.SkipReason)
		} else if att.Err != nil && att.Kind == "" {
			dc.Kind = string(KindUnknown)
		}
		d.Candidates = append(d.Candidates, dc)
	}

	switch {
	case err == nil && len(attempts) > 0:
		d.Success = true
		last := attempts[len(attempts)-1]
		d.ChosenProvider = last.Provider
		d.ResolvedModel = last.Model
		if len(attempts) == 1 {
			d.ReasonCodes = append(d.ReasonCodes, ReasonFirstSuccess)
		} else {
			d.ReasonCodes = append(d.ReasonCodes, ReasonFailoverChosen)
		}
	case ctx.Err() != nil:
		d.ReasonCodes = append(d.ReasonCodes, ReasonContextCanceled)
	default:
		d.ReasonCodes = append(d.ReasonCodes, ReasonAllExhausted)
	}
	return resp, d, err
}

// classifySkipReason 把 skipIfBlocked 的 reason 文本归类为跳过码。
// 真实 reason 由 resilience.go/quota.go 产出，格式固定：
//
//	"cooldown until …"  / "breaker open (…)"  / "model locked until …"
//	/ "quota <rpm|rpd|tpm|tpd> exhausted (…)"
//
// 按真实词根精确前缀匹配；未识别保守归熔断（存在阻断事实，来源不明）。
// 切片二把 Block/Blocked 改为返回结构化码后此处直读。
func classifySkipReason(reason string) ReasonCode {
	switch {
	case strings.HasPrefix(reason, "cooldown"), strings.HasPrefix(reason, "breaker"):
		return ReasonCircuitOpen // breaker 与 cooldown 同为连接层熔断/冷却语义
	case strings.HasPrefix(reason, "model locked"):
		return ReasonModelLocked
	case strings.HasPrefix(reason, "quota"):
		return ReasonQuotaWindow
	}
	return ReasonCircuitOpen
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
