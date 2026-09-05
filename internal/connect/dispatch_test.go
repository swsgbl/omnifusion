package connect

import "testing"

// TestOriginWildcardsToLoopback 通配/空监听地址对客户端回环；端口必须
// 保留——管家/CLI 写入的接入地址缺端口会指向 80 而失败。
func TestOriginWildcardsToLoopback(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{"0.0.0.0", 20130, "http://127.0.0.1:20130"},
		{"", 20130, "http://127.0.0.1:20130"},
		{"::", 20130, "http://127.0.0.1:20130"},
		{"192.168.1.5", 8787, "http://192.168.1.5:8787"},
	}
	for _, c := range cases {
		if got := Origin(c.host, c.port); got != c.want {
			t.Errorf("Origin(%q,%d) = %q, want %q", c.host, c.port, got, c.want)
		}
	}
}

// TestScanReportsKnownTools Scan 至少返回全部已知目标，且字段齐备。
func TestScanReportsKnownTools(t *testing.T) {
	tools := Scan("http://127.0.0.1:20130")
	if len(tools) < len(Targets) {
		t.Fatalf("scan returned %d tools, want >= %d", len(tools), len(Targets))
	}
	seen := map[string]bool{}
	for _, tool := range tools {
		seen[tool.ID] = true
		if tool.ID == "" || tool.Name == "" || tool.ConfigPath == "" {
			t.Errorf("tool %q incomplete: %+v", tool.ID, tool)
		}
	}
	for _, id := range TargetIDs() {
		if !seen[id] {
			t.Errorf("scan missing target %q", id)
		}
	}
}
