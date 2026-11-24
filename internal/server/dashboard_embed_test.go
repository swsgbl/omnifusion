package server

import (
	"strings"
	"testing"
)

// TestDashboardPagesEmbedGuard 桌面端内嵌时（?embed=1）六页都必须隐藏
// 页内导航/语言切换/跳转链接（壳是唯一控制源，v0.1.3 实测双控制源
// 重复）；浏览器直开（无 embed 参数）保持原样。本测试防六页代码漂移。
//
// 断言的是结构而不是某一行原文：内嵌分支先隐藏导航，其后才是浏览器
// 分支，导航与语言链接只允许在浏览器分支里生成。全部用单行片段匹配，
// 不受工作区行尾（CRLF / LF）影响。
func TestDashboardPagesEmbedGuard(t *testing.T) {
	for _, page := range []string{"chat", "providers", "keys", "usage", "compression", "resilience"} {
		data, err := dashboardFS.ReadFile("dashboard/" + page + ".html")
		if err != nil {
			t.Fatalf("read %s: %v", page, err)
		}
		html := string(data)
		if !strings.Contains(html, "has('embed')") {
			t.Errorf("%s: missing EMBED guard", page)
		}
		after := func(from int, needle string) int {
			i := strings.Index(html[from:], needle)
			if i < 0 {
				return -1
			}
			return from + i
		}
		iBranch := strings.Index(html, "if (EMBED) {")
		iHide := after(iBranch, "nav.hidden = true;")
		iSkip := after(iHide, "document.querySelector('.skip').hidden = true;")
		iElse := after(iSkip, "} else {")
		iFill := after(iElse, "nav.replaceChildren(")
		if iBranch < 0 || iHide < 0 || iSkip < 0 || iElse < 0 || iFill < 0 {
			t.Errorf("%s: nav must be hidden in the EMBED branch and filled only in the browser branch (branch=%d hide=%d skip=%d else=%d fill=%d)",
				page, iBranch, iHide, iSkip, iElse, iFill)
			continue
		}
		// 语言切换链接也只属于浏览器分支。
		if tail := html[iElse:]; !strings.Contains(tail, "'中文'") || !strings.Contains(tail, "'EN'") {
			t.Errorf("%s: language switch links must be built in the browser branch", page)
		}
	}
}
