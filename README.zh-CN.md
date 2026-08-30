# OmniFusion

[English](README.md) | **简体中文**

> BYOK 优先 · Go 单二进制 · 免费额度最大化 —— 高性能 AI 网关
>
> 以 Bifrost 级性能底座（Go，µs 级开销）承载 OmniRoute 级功能深度（多策略路由、三层弹性隔离、Token 压缩管线、MCP）。

**一句话**：把 12 家 LLM 提供商的免费额度聚合成一个本地端点（OpenAI / Anthropic / Gemini / Responses 四协议入站），自带对话页、双语控制台、桌面应用与 CLI——装完即聊，永不断流。

## 它解决什么问题

把多个 LLM 提供商（优先免费层，BYOK 自带密钥）聚合到一个本地 OpenAI 兼容端点：

- **永不断流**：三层故障隔离（Provider 断路器 ⊃ Key 冷却 ⊃ Model 锁定）+ buffer-first-chunk（首 chunk 前自动切换上游，首 chunk 后保流不断）；
- **额度最大化**：多维打分路由（健康·延迟·剩余配额）自动榨干每个免费层，候选按模型成员过滤（目录不供该模型的家直接跳过，新会话首请求不吃无效上游超时）；能力排序路由（`@quality`）按社区目录能力分由强到弱自动选最强免费模型；
- **Token 压缩**：Session-Dedup → 工具输出折叠 → Caveman 规则压缩，可选 LLMLingua-2 语义压缩，全部经 Fidelity Gate 防劣化；
- **Agent 原生**：内置 MCP Server + `ofd run claude` 一键绑定；
- **可信**：本地优先、密钥 AES-256-GCM 加密、无遥测、默认仅监听回环地址。

## 文档

- 用户可感知的变更见 [CHANGELOG.md](CHANGELOG.md)；
- 部署与编排：[deploy/README.md](deploy/README.md)（Docker/compose/systemd/Prometheus/Grafana，含零密钥 mock 验证栈与冒烟脚本）；
- 桌面端构建：[apps/desktop](apps/desktop)（build.cmd）。

## 快速开始

**方式一：下载即用（推荐普通用户）**

1. 从 [Releases](https://github.com/swsgbl/omnifusion/releases) 下载对应平台的 `ofd`（Windows 为 `ofd.exe`）；桌面用户直接装 `OmniFusion.Desktop` 安装包（自带网关，装完即用）；
2. 申请一个免费密钥（如 [OpenRouter](https://openrouter.ai/keys)），录入：`ofd key add openrouter`（交互式，输入隐藏）；
3. 启动 `ofd`，按控制台指引打开 `http://127.0.0.1:20130/dashboard/chat?key=$(ofd gateway-key)` ——**内置对话页，默认「⚡ 自动」按能力选最强免费模型**，装完即聊。

**方式二：从源码构建（开发者）**

```bash
go build -o ofd ./cmd/ofd
./ofd serve                     # 默认 127.0.0.1:20130，配置模板见 config.yaml.example
```

**接入你已有的客户端**（Claude Code / Codex / Gemini CLI 一条命令，自动拉起网关）：

```bash
ofd run claude                  # 注入环境变量并拉起官方 CLI
ofd gateway-key                 # 打印数据面令牌（ofg-…），任意 OpenAI 兼容客户端可用：
                                #   base_url: http://127.0.0.1:20130/v1
```

**零真实 key 冒烟**（mock 上游全链路 17 断言）：`sh scripts/smoke.sh`（Windows 亦可 `powershell -File scripts/smoke.ps1`）

**容器化**（含 mock 验证栈 / Prometheus+Grafana，见 deploy/README.md）：

```bash
docker compose -f deploy/docker-compose.yml --profile mock up -d --build
```

## License

Apache-2.0（见 [LICENSE](LICENSE)）
