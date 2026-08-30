# 更新日志（Changelog）

本文件记录用户可感知的变更；格式遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号语义化（SemVer）。

## [v0.1.2] - 2026-08-30

### 新增

- **国内三件套出厂预装**：智谱 BigModel（GLM-4.5-Flash / GLM-4.7-Flash 完全免费、128K）、硅基流动（L0 免费档 16 模型）、魔搭 ModelScope（每日 2000 次免费调用）——全部 OpenAI 兼容端点、国内直连，与 Groq 类区域封锁形成双通道（国内档直连、西方档走代理）；内置提供商 9 → 12；
- **cheap 真成本路由**：注册表 / 签名 feed 登记每模型定价（USD / 1M tokens；显式 0 = 免费声明，省略 = 未登记），cheap 策略升级为三档真成本排序——登记免费 → 未登记 → 已定价按预计成本升序（输入单价×压缩后 token + 输出单价×名义输出长度），档内保持免费额度余量降序；无定价数据时整体退回 v1 余量语义。存量 9 家同步标注（免费层内 0 价、anthropic 公开单价、未核实费率省略）；feed 新增 `price_in/price_out` 字段（指针语义同注册表，负值/半配对拒收）。

> EN: Three CN providers preinstalled (Zhipu / SiliconFlow / ModelScope — direct-connect free tiers, 9 → 12 built-ins); cheap strategy upgraded to true-cost ordering over declared per-model prices (registry YAML + signed feed `price_in`/`price_out`).

## [v0.1.1] - 2026-08-29

「小白友好」轮：把复杂性继续留在底层，把首次成功体验补到交互层。

> EN: The "novice-friendly" release — OpenAI Responses API inbound, `@quality` capability-ranked routing, built-in dashboard chat page, first-run guide, desktop app with bundled gateway & key management.

### 新增

- **OpenAI Responses API 入站（`POST /v1/responses`）**：Codex CLI 默认 wire 协议与新一代 OpenAI SDK 零配置直连——input（字符串/item 数组）与 instructions 归一进网关 IR，工具定义/调用/结果三向互译，`text.format` 结构化输出映射，reasoning/metadata 等无对应字段显式降级标头；流式输出完整 Responses SSE 事件序列，断流优雅收尾。协议矩阵第四入站面——协议翻译矩阵全格落地；
- **quality 能力排序路由**：`@quality:model` 候选按社区签名 feed 的模型能力分（0-100，新增 `capability` 字段）由强到弱自动排序——「免费模型路由器」的能力排序半边补齐；无 feed/未评级时保持注册序不阻断，隔离/配额/窗口过滤照常生效。**裸 `@quality`（自动选最强）**：无需目标模型——每家取其能力分最高的模型、按分降序逐尝试；对话页「自动」选项即此（无能力数据时边界 400 明示）；
- **Dashboard 对话页**（`/dashboard/chat`）：内置流式对话界面——模型下拉自动列出目录清单，默认「⚡ 自动（最强优先）」；中英双语、会话保留在本页、密钥复用 `?key=` 机制。装完即聊，不再要求先会用 Claude Code/Cursor。顺带修复五页互链的 `.html` 后缀 404（历史 bug，路由改为双形态兼容）；
- **首启引导**：零密钥启动时控制台输出双语三步指引（申请免费 key → `ofd key add` → 打开对话页 URL），并提示 `ofd run claude/codex` 一键接入；
- **桌面端密钥管理**：提供商下拉 +「添加/更新密钥」按钮，在可见控制台窗口拉起交互式 `ofd key add`（密钥输入隐藏）；桌面端新增「对话」标签页；
- **桌面端捆绑网关**：安装包经 tauri resources 自带 `ofd.exe`（build.cmd 构建时自动打包），bin 留空自动解析安装目录副本——小白无需单独下载网关。

### 改进

- 上游 403 错误消息附带区域封锁提示与对策（`HTTPS_PROXY` 或换 provider）——Groq 类地区封锁不再是一句裸状态码。

## [v0.1.0] - 2026-08-29

首个功能完整版：聚合网关 → 三协议互译 → 压缩与缓存 → 弹性与可观测 → 智能路由，全部核心能力一次到位。

> EN: First feature-complete release — aggregation core, three-layer resilience, multi-protocol translation, token compression + semantic cache, MCP/A2A/CLI, Fusion + ML routing.

### 新增

- **聚合网关核心**：多 provider 聚合到本地 OpenAI 兼容端点（`/v1/chat/completions`、`/v1/messages`、`/v1beta/models/*`），BYOK 密钥 AES-256-GCM 加密落盘、默认仅监听回环；
- **三层弹性隔离**：Provider 断路器 ⊃ Key 冷却 ⊃ Model 锁定，buffer-first-chunk（首 chunk 前自动切换上游、首 chunk 后保流不断流），断流三协议优雅收尾；
- **多维路由**：打分策略（健康·延迟·剩余配额）、sticky 会话（30min 滑动）、组合路由（`@combo:NAME`）、ML 弱/强分档（`@smart`）、Fusion 扇出+Judge 合成（`@fusion`）；**候选按模型成员过滤**——目录不供该模型的 provider 在会话绑定前剔除，新会话首请求不再吃无效上游超时（bench A/B 整轮 28.4s→15.2s）；
- **Token 压缩管线**：Session-Dedup → 工具输出折叠 → Caveman 规则压缩，可选 LLMLingua-2 级语义压缩（sidecar 部署，失败回退原文直传），全程 Fidelity Gate 防劣化；压缩组合可按路由组合 per-path 绑定；
- **语义缓存**：精确键缓存 + 异步回写，命中零上游消耗；
- **会话记忆**：SQLite FTS5 中文可检（拉丁词+CJK bigram 双侧同口径），逐请求 opt-in（`X-OmniFusion-Memory: on`，默认关闭零落盘）；
- **Agent 面**：内置 MCP Server（11 工具，scoped token）、`ofd run claude` 一键绑定、A2A v1.0 协议端点、Tauri 桌面端（NSIS 安装包，网关托管+五页 dashboard 内嵌+托盘）；
- **可观测**：Prometheus `/metrics`（六族指标）、请求审计日志与查询 API、Dashboard 五页（providers/usage/compression/resilience/audit）、Grafana 导入即用面板；
- **签名目录 feed**：社区维护的窗口/众测数据源（Ed25519 验签 + 防回滚基线跨重启），维护者工具 `ofd catalog keygen|sign|verify|report`；
- **护栏**：规则型 PII/注入检测（默认关闭，显式启用）。

### 修复（发布前审计收口）

- 启动横幅/dashboard/MCP serverInfo 的版本号随构建正确注入（原先发布版恒显示 dev）；
- keyring 可选口令可用：设置 `OFD_KEYRING_PASSPHRASE` 后主密钥混入口令（原先选项存在但无启用入口）；
- `ofd run codex` 启动时提示 Codex 默认 `wire_api="responses"` 与网关 `/v1/chat/completions` 的协议差异及配置方法（原先聊天 404 无提示）。

### 工程化

- 黑盒基准套件（缓存命中/TTFT/200 与 1000 并发，Windows 建连斜坡）；
- 工程化收尾：部署链／冒烟脚本／基准集等均已就绪；遗留项同日落地：路由候选模型成员过滤（`c07eebb`）、config.go 与 6 个超 300 行测试文件拆分（`4c1976f`/`a8548f2`）；
- 仓库纪律：函数 ≤50 行、文件 ≤300 行、TDD 红绿、每任务一提交，`go test ./...` 20 包全绿。

[Unreleased]: 提交到 main 即视为最新；里程碑收官打 annotated tag。
