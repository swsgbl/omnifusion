package server

import (
	"net/url"
	"testing"
)

// TestCheckPublicHostBlocksPrivate 回环/内网/链路本地/非 http(s) 一律拒
//（SSRF 守卫；IP 字面量路径不依赖 DNS，测试确定性）。
func TestCheckPublicHostBlocksPrivate(t *testing.T) {
	blocked := []string{
		"http://127.0.0.1:20130/dashboard",
		"http://10.1.2.3/",
		"http://172.16.0.9/",
		"http://192.168.1.1/",
		"http://169.254.169.254/latest/meta-data",
		"http://[::1]/",
		"http://[fe80::1]/",
		"http://0.0.0.0/",
		"ftp://example.com/file",
		"file:///etc/passwd",
		"http://",
		"https://", // 无 host
	}
	for _, raw := range blocked {
		u, _ := url.Parse(raw)
		if err := checkPublicHost(u); err == nil {
			t.Errorf("accepted blocked URL %s", raw)
		}
	}
	allowed := []string{
		"http://1.1.1.1/",       // 公网 IP 字面量（校验通过，不实际拨号）
		"http://8.8.8.8:8080/x", // 公网 IP 带端口
	}
	for _, raw := range allowed {
		u, _ := url.Parse(raw)
		if err := checkPublicHost(u); err != nil {
			t.Errorf("rejected public URL %s: %v", raw, err)
		}
	}
}
