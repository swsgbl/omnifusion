// routehint.go 在 HTTP 边界解析策略选择（验收：策略可经
// model 内嵌 @指令 或 header 选择），重写 req.Model 为裸模型名。
package server

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/routing"
)

// dispatchOptions 解析逐请求路由选择：model 内嵌 @指令（in-band）优先于
// X-OmniFusion-Strategy header（out-of-band）。策略指令把 req.Model 重写
// 为裸模型名；组合指令（"@combo:NAME"）保持原样（逐尝试按成员
// 模型改写，缓存键亦按组合区分），返回组合名供调用方应用压缩绑定。
// Fusion 指令（"@fusion"）置 fusion=true 短路到 FusionRunner。
func (s *Server) dispatchOptions(r *http.Request, req *schema.UnifiedRequest) ([]routing.DispatchOption, string, bool, error) {
	name, combo := "", ""
	if directive, bare, err := routing.ParseModelDirective(req.Model); err != nil {
		return nil, "", false, err
	} else if directive != "" {
		if directive == routing.DirectiveFusion {
			if bare != "" {
				return nil, "", false, errors.New("fusion directive takes no target model, use \"@fusion\"")
			}
			return nil, "", true, nil
		}
		if directive == routing.DirectiveSmart {
			if bare != "" {
				return nil, "", false, errors.New("smart directive takes no target model, use \"@smart\"")
			}
			if s.router == nil || s.router.Smart == nil {
				return nil, "", false, errors.New("smart routing not configured (mlrouter section)")
			}
			// 仅作压缩绑定：comboName 喂 comboCompress（改写消息 +
			// WithPromptTokens），不加 WithCombo——组合路径会劫持候选
			// 选择，@smart 的候选必须来自 ML 计划。
			combo := s.defaultCombo()
			if combo != "" && !s.comboKnown(combo) {
				combo = ""
			}
			return []routing.DispatchOption{routing.WithSmart()}, combo, false, nil
		}
		if directive == routing.DirectiveQuality && bare == "" { // 裸 @quality：自动选最强
			if s.router == nil || s.router.Capability == nil {
				return nil, "", false, errors.New("quality auto needs capability data (configure catalog feed)")
			}
			return []routing.DispatchOption{routing.WithQualityAuto()}, "", false, nil
		}
		if bare == "" {
			return nil, "", false, errors.New("strategy directive needs a target model, e.g. \"@fast:llama-3.3-70b\"")
		}
		if directive == routing.DirectiveCombo {
			if !s.comboKnown(bare) {
				return nil, "", false, fmt.Errorf("unknown combo %q", bare)
			}
			combo = bare
			return []routing.DispatchOption{routing.WithCombo(combo)}, combo, false, nil
		}
		name, req.Model = directive, bare
	}
	if name == "" {
		if h := r.Header.Get(routing.HeaderStrategy); h != "" {
			directive, _, err := routing.ParseModelDirective("@" + h)
			if err != nil {
				return nil, "", false, err
			}
			if directive == routing.DirectiveSmart {
				return nil, "", false, errors.New("smart directive is model-embedded only, use \"@smart\"")
			}
			name = directive
		}
	}
	var opts []routing.DispatchOption
	if name != "" {
		if s.log != nil {
			s.log.Info("strategy selected", "strategy", name, "model", req.Model)
		}
		opts = append(opts, routing.WithStrategyName(name))
	}
	if combo == "" { // 默认压缩组合：请求未显式选组合时应用
		if dc := s.defaultCombo(); dc != "" && s.comboKnown(dc) {
			combo = dc
			opts = append(opts, routing.WithCombo(dc))
		}
	}
	return opts, combo, false, nil
}

// sessionOption 从 X-Session-Id 提取会话亲和；无头则不加。
func sessionOption(r *http.Request) []routing.DispatchOption {
	if id := r.Header.Get(routing.HeaderSession); id != "" {
		return []routing.DispatchOption{routing.WithSession(id)}
	}
	return nil
}
