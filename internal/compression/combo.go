// combo.go 是压缩组合工厂：按阶段名列表构造管线，供 YAML
// 配置装配（压缩组合绑定路由组合——per-path 压缩策略）。
package compression

import (
	"fmt"
	"sort"
)

// 阶段注册表：名字 → 默认配置构造器。新增阶段在此登记即可被组合引用。
var stageBuilders = map[string]func() CompressionStage{
	"dedup":            func() CompressionStage { return NewDedupStage(DedupConfig{}) },
	"toolfilter":       func() CompressionStage { return NewToolFilterStage(ToolFilterConfig{}) },
	"caveman":          func() CompressionStage { return NewCavemanStage(CavemanConfig{}) },
	"semantic":         func() CompressionStage { return NewSemanticStage(SemanticConfig{}) },
	"semantic_sidecar": func() CompressionStage { return NewSidecarStage() },
}

// StageNames 返回可用阶段名（排序稳定，供错误信息与文档）。
func StageNames() []string {
	names := make([]string, 0, len(stageBuilders))
	for n := range stageBuilders {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// BuildCombo 按有序阶段名构造压缩管线（默认 Fidelity Gate）。
// 空列表或未知名报错——组合是静态配置，错误应在装配期暴露。
func BuildCombo(stageNames []string) (*Pipeline, error) {
	if len(stageNames) == 0 {
		return nil, fmt.Errorf("compression: combo needs at least one stage (available: %v)", StageNames())
	}
	stages := make([]CompressionStage, 0, len(stageNames))
	for _, n := range stageNames {
		build, ok := stageBuilders[n]
		if !ok {
			return nil, fmt.Errorf("compression: unknown stage %q (available: %v)", n, StageNames())
		}
		stages = append(stages, build())
	}
	return NewPipeline(nil, stages...), nil
}
