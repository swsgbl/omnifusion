# deploy — 部署与可观测性资产

## 容器部署（Docker Compose，推荐）

三档 profile 按需叠加，端口默认只绑宿主回环（安全红线）：

```bash
# 基础栈：仅网关（先 docker build 或由 compose 自动构建）
docker compose -f deploy/docker-compose.yml up -d --build

# mock 栈：无任何真实 API key 的全链路验证
#   mockup 与网关共享网络命名空间——内置 ollama provider 的
#   localhost:11434 天然指向 mock，零产品代码改动
docker compose -f deploy/docker-compose.yml --profile mock up -d --build

# 可观测栈：Prometheus + Grafana（通常叠加 mock 一起验证）
docker compose -f deploy/docker-compose.yml --profile mock --profile obs up -d --build
```

手工构建镜像（国内网络走 goproxy.cn）：

```bash
docker build --build-arg GOPROXY=https://goproxy.cn,direct \
  -f deploy/Dockerfile -t omnifusion:latest .
docker build --build-arg GOPROXY=https://goproxy.cn,direct \
  -f deploy/Dockerfile.mockup -t omnifusion-mock:latest .
```

镜像要点：两阶段构建（golang:1.26-alpine → alpine:3.22）、
`CGO_ENABLED=0` 静态编译（sqlite 为纯 Go 的 modernc 驱动）、非 root
用户（uid 10001 ofd）、内置 HEALTHCHECK（/healthz）、`/data` 卷持久化
连接与缓存。

### mock 栈验证流程

```bash
docker compose -f deploy/docker-compose.yml --profile mock up -d
KEY=$(docker exec omnifusion ofd gateway-key)     # 容器内机器身份派生
curl -s http://127.0.0.1:20130/healthz            # 立即可用
curl -s -H "Authorization: Bearer $KEY" \
  http://127.0.0.1:20130/v1/models                # 目录冷启动短退避后 ≤60s 出 mock-model-1
curl -s -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"mock-model-1","messages":[{"role":"user","content":"hi"}]}' \
  http://127.0.0.1:20130/v1/chat/completions
docker compose -f deploy/docker-compose.yml --profile mock down -v
```

容器配置 `config.docker.yaml` 已改绑 `0.0.0.0`（进程只活在容器网络里，
对外暴露面由 compose ports 左侧控制）。密钥不进配置文件：进容器执行
`ofd key add <provider> --stdin` 加密落 `/data/omnifusion.db`（卷持久化，
重启不丢）。

## 裸机部署（systemd）

见 `systemd/ofod.service` 头部注释的六步安装（二进制 → 系统用户 →
配置（host 保持 127.0.0.1）→ 单元文件 → enable --now → journalctl）。
单元含最小权限加固块（NoNewPrivileges / ProtectSystem=strict /
ProtectHome / PrivateTmp 等）。

## Prometheus 抓取

复制 `prometheus/omnifusion.yml`，用 `ofd gateway-key` 输出的 key 替换
`credentials`（/metrics 与数据面同令牌，Bearer 鉴权），`targets` 指向网关
监听地址。网关配置里 `metrics.enabled: false` 可整体关闭 /metrics。
compose obs profile 中 targets 需写 `omnifusion:20130`。

## Grafana 看板

Dashboards → New → Import → 上传 `grafana/omnifusion-dashboard.json`，
数据源选指向上述 Prometheus 的实例。面板全部基于网关原生指标：

> Prometheus 文本协议对零样本族连 HELP/TYPE 都不输出：刚启动或尚未
> 发生对应事件（如护栏拦截）时，相关面板显示 no data 是正常行为，
> 首个事件发生后自动出线。

| 面板 | 指标 |
|---|---|
| 请求速率（端点/状态） | `omnifusion_requests_total` |
| 时延 p50/p95 | `omnifusion_request_duration_seconds` |
| 首 token p50/p95 | `omnifusion_ttft_seconds` |
| token 吞吐 | `omnifusion_tokens_total` |
| 语义缓存命中率 | `omnifusion_requests_total{provider="cache"}` 占比 |
| 上游失败（类别） | `omnifusion_upstream_failures_total` |
| 护栏发现 | `omnifusion_guardrails_findings_total` |
| 网关进程 | `go_goroutines` / `process_resident_memory_bytes` |

## 请求审计日志（非 Prometheus 面）

`GET /dashboard/api/audit?limit=&since=&provider=&endpoint=`（scope=audit
或 master key）逐行读 `request_log` 表；MCP 工具 `omnifusion_audit_recent`
同口径。
