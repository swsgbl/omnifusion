// report.go 是压缩管线的任务级保真证据（蓝图 Phase 5：Fidelity Gate
// 不止 pass/fail——输出可验证证据面：结构解析成功率、工具参数一致
// 性、system 完整性、压缩比与逐阶段失败类型。证据由确定性规则计算，
// 供日志/排障/验收对账，绝不作为路由输入）。
package compression

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// FidelityReport 是一轮管线执行的证据集合（按阶段顺序）。
type FidelityReport struct {
	Stages []StageFidelity
}

// StageFidelity 是单阶段的证据快照：结构化值覆盖率、工具参数一致、
// system 完整、压缩比与拦截原因（失败类型=命中规则名）。
type StageFidelity struct {
	// Stage 是阶段名（与 CompressionStats.Stage 对齐）。
	Stage string
	// Applied / Skipped 与 stats 对齐（两者互斥；都 false=阶段 Err）。
	Applied bool
	Skipped bool
	// Err 非空表示阶段自身失败原文直传（保真完整，只是没压）。
	Err string
	// Rejection 是命中的门规则名（失败类型）；空=未拦截。
	Rejection string
	// StructuredTotal / StructuredKept：进入本阶段的结构化值（唯一
	// JSON + 围栏代码块指纹）总数，与产出中存活的份数（唯一值口径：
	// 重复折叠 2→1 合法计存活）。
	StructuredTotal int
	StructuredKept  int
	// ToolArgsIntact：本阶段输入的全部 tool_calls（id/name/arguments）
	// 在产出中原样存在——蓝图硬规则：工具参数默认零语义压缩。
	ToolArgsIntact bool
	// SystemPreserved：system 消息多重集不减。
	SystemPreserved bool
	// CompressionRatio：产出/输入粗估 token（跳过/失败/拦截=1.0）。
	CompressionRatio float64
}

// RunWithReport 执行管线并逐阶段取证（Run 的证据增强版；Run 保持
// 原签名，既有调用方零变化）。
func (p *Pipeline) RunWithReport(sc *StageContext, msgs []schema.Message) ([]schema.Message, []CompressionStats, *FidelityReport) {
	cur := msgs
	stats := make([]CompressionStats, 0, len(p.stages))
	rep := &FidelityReport{Stages: make([]StageFidelity, 0, len(p.stages))}
	for _, st := range p.stages {
		next, s := p.applyStage(st, sc, cur)
		stats = append(stats, s)
		rep.Stages = append(rep.Stages, stageFidelityOf(s, cur, next))
		cur = next
	}
	return cur, stats, rep
}

// stageFidelityOf 从阶段统计与进出消息计算证据快照。
func stageFidelityOf(s CompressionStats, before, after []schema.Message) StageFidelity {
	f := StageFidelity{
		Stage:           s.Stage,
		Applied:         s.Applied,
		Skipped:         s.Skipped,
		ToolArgsIntact:  toolCallsIntact(before, after),
		SystemPreserved: systemPreserved(before, after) == nil,
	}
	if s.Err != nil {
		f.Err = s.Err.Error()
	}
	var rej *GateRejection
	if errors.As(s.GateRejected, &rej) {
		f.Rejection = rej.Rule
	}
	f.StructuredTotal, f.StructuredKept = structuredCoverage(before, after)
	f.CompressionRatio = 1.0
	if s.BeforeTokens > 0 && s.Applied {
		f.CompressionRatio = float64(s.AfterTokens) / float64(s.BeforeTokens)
	}
	return f
}

// toolCallsIntact 判定 before 的全部 tool_calls 在 after 中原样存在
// （与门规则 tool_calls_preserved 同判定，证据面取布尔）。
func toolCallsIntact(before, after []schema.Message) bool {
	return toolCallsPreserved(before, after) == nil
}

// structuredCoverage 统计唯一结构化值的总数与存活数（重复折叠合法）。
func structuredCoverage(before, after []schema.Message) (total, kept int) {
	jsonBefore, codeBefore := collectStructured(before)
	jsonAfter, codeAfter := collectStructured(after)
	for k := range jsonBefore {
		total++
		if jsonAfter[k] > 0 {
			kept++
		}
	}
	for k := range codeBefore {
		total++
		if codeAfter[k] > 0 {
			kept++
		}
	}
	return total, kept
}

// Summary 返回单行证据摘要（日志/排障用，与 RouteDecision.Summary
// 同风格）："stages=2 applied=1 struct=4/4 toolargs=ok sys=ok"。
func (r *FidelityReport) Summary() string {
	if r == nil {
		return "stages=0"
	}
	applied := 0
	for _, f := range r.Stages {
		if f.Applied {
			applied++
		}
	}
	total, kept := 0, 0
	toolArgs, sys := true, true
	for _, f := range r.Stages {
		total += f.StructuredTotal
		kept += f.StructuredKept
		toolArgs = toolArgs && f.ToolArgsIntact
		sys = sys && f.SystemPreserved
	}
	return fmt.Sprintf("stages=%d applied=%d struct=%d/%d toolargs=%s sys=%s",
		len(r.Stages), applied, kept, total, boolTag(toolArgs), boolTag(sys))
}

// StageSummary 返回单阶段摘要（含失败类型），如
// "folding ok ratio=0.42" 或 "semantic rejected=structured_integrity ratio=1.00"。
func (f StageFidelity) Summary() string {
	status := "ok"
	switch {
	case f.Skipped:
		status = "skipped"
	case f.Err != "":
		status = "error"
	case f.Rejection != "":
		status = "rejected=" + f.Rejection
	}
	return f.Stage + " " + status + " ratio=" + strconv.FormatFloat(f.CompressionRatio, 'f', 2, 64)
}

func boolTag(b bool) string {
	if b {
		return "ok"
	}
	return "LOSS"
}
