// modelfilter.go 是模型成员过滤（ 遗留项落地）：裸模型
// 请求只尝试目录声明可服务该模型的 provider。动机（bench 实证）：
// 候选序把不可达/被墙 provider（静态回落清单）排在前面时，每个新
// 会话的首请求要吃满一次上游超时（~25s）才回退到真正持有该模型的
// 家。保守边界与 filterByWindow 同哲学：无快照不过滤、全排除回退
// 未过滤列表（宁可让上游拒绝，不能全拒）。
//
// 已知局限（当前零声明，声明后需并入判定）：registry YAML 的
// model_aliases 重写不参与匹配；「厂商/模型」后缀规则覆盖
// OpenRouter 风格的限定 id（请求裸名、目录收录限定名）。
package routing

import (
	"strings"

	"github.com/swsgbl/omnifusion/internal/provider"
)

// ModelMembership 供给 (provider, model) 的可服务性判定（保守语义：
// 不确定——目录未同步/未收录 provider——返回 true，不因过滤被排除）。
// 生产实现是 Catalog（live 目录 + 静态回落，同 Windows 的双实现）。
type ModelMembership interface {
	ServesModel(providerName, model string) bool
}

// ModelGate 是用户侧的模型启停开关（providers 页可编辑，2026-10-01
// 用户需求）：返回给定 provider 的禁用模型集（nil/空=全启用）。与
// ModelMembership 正交——目录说"能服务"、用户说"别用它"，gate 赢；
// 保守边界同上：gate 未装配或集合为空不过滤，全禁用回退未过滤。
type ModelGate interface {
	DisabledModels(providerName string) map[string]bool
}

// filterByModel 排除目录明确不服务该模型的候选，再排除用户显式停用
// 的模型。cands 不会被就地修改；全排除时原样返回（保守回退：可能
// 是目录未同步或别名场景）。
func (r *Router) filterByModel(cands []provider.Provider, model string) []provider.Provider {
	if r.Models == nil || model == "" || len(cands) <= 1 {
		return cands
	}
	kept := make([]provider.Provider, 0, len(cands))
	var dropped []string
	for _, p := range cands {
		if !r.Models.ServesModel(p.Name(), model) {
			dropped = append(dropped, p.Name())
			continue
		}
		if r.Gate != nil {
			if dis := r.Gate.DisabledModels(p.Name()); dis[model] {
				dropped = append(dropped, p.Name()+"(disabled)")
				continue
			}
		}
		kept = append(kept, p)
	}
	if len(kept) == 0 {
		return cands
	}
	if r.Log != nil && len(dropped) > 0 {
		r.Log.Debug("model membership filtered candidates",
			"model", model, "kept", len(kept), "dropped", strings.Join(dropped, ","))
	}
	return kept
}
