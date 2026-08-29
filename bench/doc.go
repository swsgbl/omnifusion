// Package bench 承载 OmniFusion 网关的黑盒性能基准集（docs/05 工作流
// 纪律第 4 条：流式 TTFT、缓存命中延迟、并发连接压测，每个里程碑对比
// 不回退）。基准以 `go build` 出的真实二进制 + 纯 net/http 驱动，
// 不 import internal/*，与生产部署形态一致。
package bench
