// degraded.go 是 的降级标记汇聚面：入站协议字段降级（translate
// From* 返回）与出站上游能力降级（ProviderCall.Degraded → Attempt）
// 在 X-OmniFusion-Degraded 响应头合并呈现——禁止静默丢弃。
package server

import (
	"github.com/swsgbl/omnifusion/internal/routing"
)

// attemptDegraded 取成功尝试的出站降级清单：回退链上只有最终服务
// 那家的清单有效，失败家的降级与本响应无关。
func attemptDegraded(attempts []routing.Attempt) []string {
	for i := len(attempts) - 1; i >= 0; i-- {
		if attempts[i].Err == nil {
			return attempts[i].Degraded
		}
	}
	return nil
}

// mergeDegraded 合并入站与出站降级清单（去重保序）。
func mergeDegraded(lists ...[]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, list := range lists {
		for _, f := range list {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	return out
}
