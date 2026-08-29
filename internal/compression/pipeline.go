// pipeline.go 是压缩管线执行器（docs/04 §4.3）：顺序执行阶段链，
// 每个阶段产出过 Fidelity Gate 才被采纳；阶段失败（含 panic，docs/04
// §5 规则 3）或被拦截都回退到该阶段输入继续——压缩永不阻断请求。
package compression

import (
	"fmt"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// Pipeline 是一条有序压缩阶段链 + 固定尾置 Fidelity Gate。
type Pipeline struct {
	stages []CompressionStage
	gate   *FidelityGate
}

// NewPipeline 组装管线；gate 为 nil 时使用默认规则集。
func NewPipeline(gate *FidelityGate, stages ...CompressionStage) *Pipeline {
	if gate == nil {
		gate = DefaultFidelityGate()
	}
	return &Pipeline{stages: stages, gate: gate}
}

// StageNames 返回阶段名链（M5.2 控制面组合清单展示用）。
func (p *Pipeline) StageNames() []string {
	out := make([]string, 0, len(p.stages))
	for _, st := range p.stages {
		out = append(out, st.Name())
	}
	return out
}

// Run 顺序执行阶段链，返回最终消息与逐阶段统计。错误不上抛
// （计入 stats 的 Err/GateRejected），输入切片永不被就地修改。
func (p *Pipeline) Run(sc *StageContext, msgs []schema.Message) ([]schema.Message, []CompressionStats) {
	cur := msgs
	stats := make([]CompressionStats, 0, len(p.stages))
	for _, st := range p.stages {
		next, s := p.applyStage(st, sc, cur)
		stats = append(stats, s)
		cur = next
	}
	return cur, stats
}

// applyStage 执行单阶段并兜底：ShouldRun=false 跳过；Apply 出错或
// panic 回退原文；Gate 拦截丢弃产出——三种情况都返回本阶段输入。
func (p *Pipeline) applyStage(st CompressionStage, sc *StageContext, msgs []schema.Message) (out []schema.Message, s CompressionStats) {
	s = CompressionStats{Stage: st.Name(), BeforeTokens: EstimateTokens(msgs)}
	defer func() {
		if r := recover(); r != nil {
			s.Err = fmt.Errorf("stage %s panicked: %v", st.Name(), r)
			s.AfterTokens = s.BeforeTokens
			out = msgs
		}
	}()

	if !st.ShouldRun(sc) {
		s.Skipped = true
		s.AfterTokens = s.BeforeTokens
		return msgs, s
	}
	applied, _, err := st.Apply(msgs)
	if err != nil {
		s.Err = err
		s.AfterTokens = s.BeforeTokens
		return msgs, s
	}
	if rej := p.gate.Check(msgs, applied); rej != nil {
		s.GateRejected = rej
		s.AfterTokens = s.BeforeTokens
		return msgs, s
	}
	s.Applied = true
	s.AfterTokens = EstimateTokens(applied)
	s.Saved = s.BeforeTokens - s.AfterTokens
	return applied, s
}
