// combos.go 承载 组合装配：YAML 声明的路由组合（命名模型组）
// 与压缩组合在此物化为 routing.Combo 表 + server 压缩管线表；
// Fusion 扇出组同文件装配（相邻语义：成员表 → 运行时结构）。
package main

import (
	"fmt"
	"log/slog"

	"github.com/swsgbl/omnifusion/internal/compression"
	"github.com/swsgbl/omnifusion/internal/config"
	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/intelligence"
	"github.com/swsgbl/omnifusion/internal/routing"
)

// buildCombos 从配置构建组合层：
// - combos：路由组合名 → routing.Combo（成员声明序即尝试优先级）；
// - pipes：组合名 → 绑定的压缩管线（nil = 纯路由组合不压缩），
// 键集合即 server 边界的「已知组合名」。
//
// 结构合法性（成员非空、绑定存在）已在 config.Load 校验；阶段名
// 合法性由 compression.BuildCombo 在此校验——配置语义错误 fail-fast。
func buildCombos(cfg *config.Config) (map[string]routing.Combo, map[string]*compression.Pipeline, error) {
	combos := make(map[string]routing.Combo, len(cfg.Combos.Routing))
	pipes := make(map[string]*compression.Pipeline, len(cfg.Combos.Routing))
	for name, rc := range cfg.Combos.Routing {
		members := make([]routing.ComboMember, 0, len(rc.Members))
		for _, m := range rc.Members {
			members = append(members, routing.ComboMember{Provider: m.Provider, Model: m.Model})
		}
		combos[name] = routing.Combo{Name: name, Members: members, Compression: rc.Compression}
		if rc.Compression == "" {
			pipes[name] = nil // 纯路由组合：占键（边界可判知名）不压缩
			continue
		}
		pipe, err := compression.BuildCombo(cfg.Combos.Compression[rc.Compression])
		if err != nil {
			return nil, nil, fmt.Errorf("combo %q (compression %q): %w", name, rc.Compression, err)
		}
		pipes[name] = pipe
	}
	return combos, pipes, nil
}

// buildFusion 物化 Fusion 扇出组；空 members 返回 nil（未启用，
// "@fusion" 请求在 server 边界 400）。合法性已由 config.Validate 校验。
func buildFusion(fc config.FusionConfig, log *slog.Logger) *intelligence.FusionRunner {
	if len(fc.Members) == 0 {
		return nil
	}
	fr := &intelligence.FusionRunner{
		Quorum: fc.Quorum,
		Log:    log,
	}
	for _, m := range fc.Members {
		fr.Members = append(fr.Members, intelligence.FusionMember{Provider: m.Provider, Model: m.Model})
	}
	if fc.Judge != nil {
		fr.Judge = intelligence.FusionMember{Provider: fc.Judge.Provider, Model: fc.Judge.Model}
	}
	return fr
}

// buildMLRouter 物化 ML 路由器（"@smart" 指令）；weak/strong
// 未同时配置返回 nil（未启用，请求在边界 400）。字段与阈值区间已由
// config.Validate 校验。默认纯 Go 启发式分类器（ONNX 对比项走可选
// 构建，学习型模型不进默认二进制）。
func buildMLRouter(mc config.MLRouterConfig) *intelligence.MLRouter {
	if mc.Weak == nil || mc.Strong == nil {
		return nil
	}
	ml := intelligence.NewMLRouter(
		intelligence.MLTarget{Provider: mc.Weak.Provider, Model: mc.Weak.Model},
		intelligence.MLTarget{Provider: mc.Strong.Provider, Model: mc.Strong.Model},
	)
	if mc.Threshold > 0 {
		ml.Threshold = mc.Threshold
	}
	return ml
}

// attachSmartRouter 把 MLRouter 决策翻译为 routing.SmartPlan 注入路由
// 器（cmd 层装配：L5 intelligence 与 L3 routing 互不 import，与 
// DispatchFunc 注入同一模式）。tier/difficulty 随计划透出（日志面）。
func attachSmartRouter(router *routing.Router, ml *intelligence.MLRouter, log *slog.Logger) {
	router.Smart = func(req *schema.UnifiedRequest) routing.SmartPlan {
		d := ml.Route(req)
		if log != nil {
			log.Info("mlrouter decision", "tier", d.Tier,
				"difficulty", fmt.Sprintf("%.2f", d.Difficulty))
		}
		members := make([]routing.ComboMember, 0, len(d.Members))
		for _, m := range d.Members {
			members = append(members, routing.ComboMember{Provider: m.Provider, Model: m.Model})
		}
		return routing.SmartPlan{Members: members, Tier: d.Tier, Difficulty: d.Difficulty}
	}
}
