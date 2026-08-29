# OmniFusion 黑盒基准集（bench/）

对网关做端到端黑盒基准：不开 internal 白盒，从「编译出的 `ofd` 二进制 + 进程内
mock 上游」起一套真实环境，仅凭 HTTP + 默认配置测量。兑现 docs/05 工作流纪律第 4 条
（提交前跑基线防回退）。

## 环境自举（TestMain）

`main_test.go` 的 TestMain 在任何用例前完成：

1. 向上找仓库根，`go build -o <tmp>/ofd.exe ./cmd/ofd`（build 失败直接非零退出）；
2. 在 `127.0.0.1:11434` 起 mock 上游（ollama provider 硬编码该地址，必须占位）；
3. 探测 `127.0.0.1:20130` 可绑后，以 temp 目录为 cwd 启动网关子进程
   （全默认配置，store 落 temp 下 `data/`，天然隔离；日志重定向到 `ofd.log`）；
4. `ofd gateway-key` 取统一 API key，轮询 `/v1/models` 等模型目录同步；
5. **会话绑定预热**：固定 `X-Session-Id` 发一个请求，把 sticky 会话粘到 mock 上游。

测试结束无条件 Kill+Wait 子进程、关 mock、删 temp 目录。

### 为什么需要会话绑定预热

路由层候选**已按请求模型过滤**（`internal/routing/modelfilter.go`，commit
`c07eebb`）：目录快照不供该模型的 provider（如不可达的 huggingface）被剔除，
bench-model 的候选收敛到 ollama 一家，新会话首请求直达 mock 上游（A/B 实测整轮
28.4s → 15.2s，差值即省下的上游超时）。预热保留作确定性兜底：目录首轮同步完成前
无快照家会被保守保留，sticky 会话（30 分钟滑动续期）确保基准请求不因该竞态漂移。

## 跑法

```bash
# 快速档（默认 200 并发；端口被占时自动 skip，不失败）
go test ./bench/ -count=1

# 四项基准（缓存命中/miss、TTFT、整流）
go test ./bench/ -bench . -benchtime 10x -count=1 -run xxx

# 重载档：1000 并发 × 3 请求
OFD_BENCH_HEAVY=1 go test ./bench/ -run Test1000ConcurrentConnections -count=1 -v

# 全量套件回归（bench 以快速档并入，约 +25s）
go test ./... -count=1
```

注意：

- **`-count=1` 必加**：环境变量不参与 go test 缓存键，`OFD_BENCH_HEAVY=1` 切档
  后不加会被缓存结果骗过；`-benchtime` 放大后同理。
- **端口独占**：需独占 `127.0.0.1:11434` 与 `127.0.0.1:20130`。任一被占
  （如本地 ollama、omnifusion docker compose 栈）时全部用例走 skip 并在 stderr
  打印原因——这是设计行为，避免与真实服务抢端口；停掉占用者后重跑即可。
- Windows 下紧邻两轮压测可能撞上网关→上游连接的 TIME_WAIT（约 60s 过期），
  绑定自带 16×5s 有界重试；若端口上有真实监听者则立即失败不空等。
- `-short` 跳过并发压测（其余基准保留）。

## 用例与指标口径

| 用例 | 口径 |
|---|---|
| `BenchmarkChatNonStream` | 同请求体预热到 `X-OmniFusion-Cache: hit` 后稳态循环，全部走缓存命中路径 |
| `BenchmarkChatCacheMiss` | 每次迭代注入进程级唯一序号强制 miss，走完整链路（鉴权→缓存查 miss→路由→上游→审计→异步回写） |
| `BenchmarkStreamTTFT` | 发流式请求到读到首个 `data:` 行，自定义指标 `ttft-ns/op` 报均值 |
| `BenchmarkStreamFull` | 完整 SSE 端到端读尽并校验 `[DONE]` 收尾 |
| `Test1000ConcurrentConnections` | N goroutine × 3 个内容互异请求（互异防缓存命中），统计成功率 / wall / 吞吐 / p50 / p99 / max，断言成功率 ≥99%（冒烟门槛） |

**缓存命中延迟** = `CacheMiss` 与 `NonStream` 的 ns/op 之差（docs/05 指标 2 的口径）。
`StreamFull` 含 mock 上游 3×2ms 的 chunk 间隔（对齐 `scripts/mockup`），下限即 ~6ms。
并发压测采用**建连斜坡**（总宽 2s 错峰握手）：Windows 上 1000 个瞬时 SYN 会溢出
accept 背压被 RST——裸 net/http 对照实验同样 ~77% 被拒，属平台行为而非网关缺陷
（Linux 丢 SYN 重传无此现象）。斜坡只是错峰握手，全部连接就绪后仍并发活跃。

网关子进程内存采样仅 Linux 可用（读 `/proc/<pid>/status` VmRSS）；Windows 无等价物，
报告里会打印跳过说明，基线表未含内存列。

## 参考基线（2026-08-29 实测）

环境：Windows 11 Pro for Workstations / Intel Ultra 9 285K（24 核）/ Go 1.26.5 /
本仓库默认配置网关 + mock 上游（回环、无真实模型推理）。数字仅作同机纵向对比锚点。

| 指标 | 实测值 |
|---|---|
| 缓存命中稳态（ChatNonStream） | ~0.70 ms/op |
| 强制 miss 全链路（ChatCacheMiss） | ~1.18 ms/op（首轮有 ~151ms 冷启动离群） |
| **缓存命中延迟差** | **~0.45–0.50 ms** |
| 流式 TTFT（ttft-ns/op） | ~1.1–1.4 ms |
| 流式整流（StreamFull，含 3×2ms chunk 间隔） | ~8.7 ms/op |
| 200 并发 × 3 = 600 请求 | 600/600 成功，wall 1.99s，300.9 req/s，p50 1.05ms，p99 1.62ms，max 3.25ms |
| 1000 并发 × 3 = 3000 请求（OFD_BENCH_HEAVY=1） | 3000/3000 成功，wall 3.12s，962.2 req/s，p50 210ms，p99 954ms，max 1.40s |

1000 档延迟显著高于 200 档是排队效应（store 单连接串行写 + 单上游），非错误；
失败时报告自动附失败样例、网关进程存活/监听现场与 `ofd.log` 尾部 2048 字节。

## 文件结构

| 文件 | 职责 |
|---|---|
| `doc.go` | 包声明（使 `go build ./...` 干净通过） |
| `main_test.go` | TestMain 自举：构建、mock、网关子进程、目录等待、会话预热、清理 |
| `mockup_test.go` | 进程内 mock 上游（行为对齐 `scripts/mockup`：3 chunk + 2ms 间隔） |
| `chat_bench_test.go` | 非流式命中/miss 对照基准与共用请求助手 |
| `stream_bench_test.go` | TTFT 与整流基准 |
| `concurrent_test.go` | 并发冒烟压测与报告汇总 |
