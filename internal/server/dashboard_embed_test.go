package server

import (
	"strings"
	"testing"
)

// TestDashboardPagesEmbedGuard 桌面端内嵌时（?embed=1）六页都必须隐藏
// 页内导航/语言切换（壳是唯一控制源，v0.1.3 实测双控制源重复）；
// 浏览器直开（无 embed 参数）保持原样。本测试防六页代码漂移。
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
		if !strings.Contains(html, "if (!EMBED) document.getElementById('nav')") {
			t.Errorf("%s: nav rendering not guarded by EMBED", page)
		}
	}
}
