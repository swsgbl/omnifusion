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

// filterByModel 排除目录明确不服务该模型的候选。cands 不会被就地
// 修改；全排除时原样返回（保守回退：可能是目录未同步或别名场景）。
func (r *Router) filterByModel(cands []provider.Provider, model string) []provider.Provider {
	if r.Models == nil || model == "" || len(cands) <= 1 {
		return cands
	}
	kept := make([]provider.Provider, 0, len(cands))
	var dropped []string
	for _, p := range cands {
		if r.Models.ServesModel(p.Name(), model) {
			kept = append(kept, p)
		} else {
			dropped = append(dropped, p.Name())
		}
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
