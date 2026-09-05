package main

import (
	"testing"

	"github.com/swsgbl/omnifusion/internal/config"
)

// connectTestCfg 返回固定端口的配置（base URL 断言用）。
func connectTestCfg() *config.Config {
	cfg := config.Default()
	cfg.Server.Port = 20130
	return cfg
}

// TestClientBaseURLWildcard 归一化：通配/空监听地址对客户端回环。
// 各工具配置文件的写入/清理测试已迁至 internal/connect/writers_test.go。
func TestClientBaseURLWildcard(t *testing.T) {
	cfg := connectTestCfg()
	cfg.Server.Host = "0.0.0.0"
	if got := clientBaseURL(cfg); got != "http://127.0.0.1:20130" {
		t.Errorf("base = %q", got)
	}
}
