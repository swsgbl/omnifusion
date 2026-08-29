package server

// version 由 main 包在启动时经 SetVersion 注入（构建链只给
// cmd/ofd 打 -X main.version；不重复注入本包，避免双事实源）。
var version = "dev"

// SetVersion 记录构建版本（来自 main.version，goreleaser
// -ldflags 注入；源码构建保持 dev）。幂等，重复调用取非 dev 值。
func SetVersion(v string) {
	if v != "" && v != "dev" {
		version = v
	}
}
