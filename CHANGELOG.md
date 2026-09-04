# 更新日志（Changelog）

本文件记录用户可感知的变更；格式遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号语义化（SemVer）。

## [v0.1.6] - 2026-09-05

免费供给收官轮：24 家内置、全网免费模型目录、一键申请密钥、全站动效。

> EN: The free-supply capstone — 24 built-in providers, one-click key signup, catalog feed v3 with the full free-model pool, and a GSAP motion layer across every surface.

### 新增

- **一键申请密钥**：每家内置厂商声明官方申请页——密钥页新增「获取密钥」列（直达申请），桌面端提供商下拉旁「申请密钥 ↗」一键打开浏览器官方页；小白不再需要搜索"去哪拿 key"；
- **内置提供商 23 → 24**：腾讯混元（100 万 tokens/年免费，端点实测）；Kimi（无永久免费层）/ Fireworks（仅 $1 试用）/ DeepInfra / Baseten（无免费层）经全网核实不收录；
- **目录 feed v3（免费模型池扩充）**：openrouter 免费系全量入册（kimi-k2.6:free、glm-5.2:free、nemotron-3-ultra:free、gpt-oss-120b:free 等 14 个 :free 模型，全部 0 价），「⚡ 自动」与 `@cheap` 的免费可选面显著扩大（14 提供商 66 模型）；
- **界面微动效（GSAP 3.15 随二进制分发）**：页面骨架/表格行进场、对话气泡上浮、用量条生长、状态徽标呼吸、按钮按压回弹；`prefers-reduced-motion` 全禁用、数据轮询只动首帧；
- **自定义 provider（config `providers:` 段）**：与内置声明同构，同 id 覆盖/新 id 追加——任意 OpenAI 兼容 / anthropic / gemini 厂商零代码接入（含申请页字段）；
- 百度千帆（ERNIE-Speed/Lite 免费）、讯飞星火（Lite 永久免费）、Chutes（免费档）。

### 维护

- CLI 错误串双语（v0.1.5 未发部分一并入册）。

## [v0.1.5] - 2026-09-01

西方次优先补齐轮：内置 20 家、目录首批证据数据、CLI 错误双语。

> EN: The western second-tier round — 20 built-in providers (SambaNova / Mistral / Cohere / Together; GitHub Models retired upstream, excluded), catalog feed v2 with the first evidence-driven graduation, bilingual CLI errors.

### 新增

- **内置提供商 16 → 20**（西方次优先补齐，端点均经 2026-08-31 实测核对）：SambaNova Cloud（Developer 档每日 20M tokens 免费，公开 /models 实收 7 个模型全量预置）、Mistral La Plateforme（Experiment 免费档；本机直连受限需代理，YAML 已注明）、Cohere（Trial key：1000 次/月、20 次/分钟）、Together AI（免费层已取消，按 BYOK 付费收录，与 Anthropic/DeepSeek 同模式）。原清单中的 GitHub Models 已于 2026-07-30 完全退役，不再收录；
- **目录 feed v2（首批证据驱动数据）**：deepseek-v4-pro / v4-flash 经真实流量验证（3 次调用全成功，`ofd catalog report` 证据）从 probation 升 **stable**；SambaNova 7 个模型携免费定价与能力分入目录（14 提供商 60 模型）；
- **CLI 错误串双语**：小白高频错误（未知子命令/端口被占/配置加载失败/密钥为空/--env 与 --stdin 冲突/connect 用法与未知客户端）改为「中文（English）」形态；HTTP API 错误维持英文契约不变；
- **目录回放优先级**：拉取失败回放 store 时不再用低于内置种子版本的陈年副本覆盖随二进制分发的最新数据（实测暴露：v2 种子被旧 v1 store 覆盖）。

## [v0.1.4] - 2026-09-01

CLI 编码代理接入轮：网关不只服务 HTTP 客户端，一条命令把密钥铺进你已有的命令行工具。

> EN: The "connect your CLI agents" release — `ofd connect` wires Claude Code / Codex / Gemini CLI / OpenCode to the gateway in one command; embedded-dashboard keyboard input fixed at the root (native child webview); `@quality` data now survives restarts; single canonical data directory.

### 新增

- **`ofd connect <claude|codex|gemini|opencode>` 一键接入**：把网关地址与令牌确定性写入各家编码 CLI 的标准配置（Claude Code settings.json env 块 / Codex config.toml model_providers（wire_api="responses"，网关原生承接）/ Gemini CLI .env / OpenCode opencode.json openai-compatible 提供商，模型默认 `@quality` 自动选强）——运行时取真值、遵守各家配置目录约定（含 CLAUDE_CONFIG_DIR/CODEX_HOME/XDG 等环境变量）、写前自动备份；`ofd disconnect` 原路清除，`--print` 只打印不落盘。桌面端新增「接入 CLI 客户端」一键按钮；Codex 密钥经 OMNIFUSION_API_KEY 用户级环境变量（Windows setx / unix 追加 shell 配置）。

### 改进

- **数据存储归一（"若无必要勿增实体"）**：数据目录默认改为每用户规范位置（Windows `%LOCALAPPDATA%\OmniFusion\data`、macOS `~/Library/Application Support/OmniFusion/data`、Linux `~/.local/share/OmniFusion/data`）——终端、桌面端、任何启动方式读写**同一份**数据库，密钥/隔离状态/缓存只有一份正本，不再随工作目录漂移；首次运行自动把旧位置最新的库迁入规范位置（幂等，显式配置 `store.path` 者不受影响）；
- **桌面端启动自动读取网关令牌**：应用打开时 key 字段为空则静默从捆绑 ofd 读取并持久化——嵌入页不再出现裸 JSON 401，「装完即用」成立；
- 服务端对浏览器形态的未鉴权页面请求回双语 HTML 指引页（桌面端点「从 ofd 读取 Key」/ `?key=` / `ofd gateway-key`），页面内 fetch 保持 JSON；
- 内嵌控制台改为**单一控制源**：嵌入模式自动跳过页内导航与语言切换，语言由桌面壳统一控制，不再出现双重控件与设置互踩。

### 修复

- **桌面端内嵌对话页打不了字（根治）**：Windows WebView2 跨源 iframe 不接收键盘输入——改用原生子 webview 承载控制台页面，键盘、焦点、滚动全部原生直达；
- **`@quality` 重启后失效**（报 "no attempts recorded"）：目录 feed 同版本重放被正确拒绝后未回放已入库数据，导致后续进程能力分全空——修复为重放/拉取失败一律回放最后接受的 feed；并内置冻结种子副本（离线/首启也有基准数据）；无数据时裸 `@quality` 返回可行动的 400 提示（而非空模型名打全部上游）；
- **裸 `@cheap` 选模缺陷**：指令串泄漏进成员过滤导致候选只剩无目录提供商，且只排序不选模——修复为每家取其登记最低价模型、按真成本升序逐尝试，无价源时回可行动 400；
- **桌面端重启后首启失败**：首次运行过杀毒扫描致冷启动超过原 8s 健康窗——放宽到 20s（进程存活即持续轮询、超时不杀进程）；网关输出落盘 `data/gateway.log`，失败错误直接附日志尾部，不再黑箱。

### 维护

- **合规套件**：仓库根新增完整 Apache-2.0 LICENSE、NOTICE（直接依赖清单及许可核实）、SECURITY.md（漏洞报告只走 GitHub 私下报告通道）；README 双语新增「隐私与合规」节（本地优先/零遥测/出站仅上游与 feed）；
- README 双语新增「设计参考」节：向五个设计参考项目（Bifrost / RouteLLM / OmniRoute / FreeLLMAPI / FreeRide）致谢并声明独立实现非 fork；MIT 义务已在 NOTICE 履行；
- 新增真机验收矩阵脚本（45 项覆盖四协议/八策略/鉴权/缓存/CLI），供发版前全量回归。

## [v0.1.3] - 2026-08-30

### 新增

- **内置提供商 12 → 16**：DeepSeek（V4 系）、通义千问·阿里云百炼（新人限时额度）、小米 MiMo（V2.5 系）、火山方舟·豆包（模型需在方舟控制台开通）——四家端点/模型均经真实密钥实测核对；
- **官方签名目录 feed v1 默认启用**：仓库 `catalog/feed.json`（13 提供商 53 模型的能力分/上下文窗口/免费定价，Ed25519 签名 + 防回滚）经 raw.githubusercontent 分发，网关默认 pin 公钥拉取——**对话页「⚡ 自动」与 `@quality` 开箱即用**（实测自动选中能力分最高的模型完成真实对话）；摄取器支持 `<url>.sig` 边车签名回退（静态托管无自定义响应头也能分发）；拉取失败照旧降级不阻断；
- **根路径落地页**：直接访问 `http://127.0.0.1:20130/` 不再 404——双语状态页带对话页/控制台/API 入口。

### 修复

- **桌面端全新安装启动网关失败**（`ofd.exe: program not found`）：路径留空时前端预填裸名短路了后端「安装目录捆绑副本优先」解析——修复为原样传空；并给 iframe 增加「网关未运行」双语引导遮罩（替代白屏）；
- NVIDIA NIM 静态模型清单对齐 live 目录（2 个已下架型号移除）。

### 维护

- 公开源码卫生：移除全部内部里程碑编号与指向内部文档的死链引用（206 文件），README 状态行改为用户视角。

> EN: 16 built-in providers (DeepSeek / Qwen Bailian / Xiaomi MiMo / Volcengine Ark added); official signed catalog feed v1 on by default — `@quality` auto-ranking works out of the box; root landing page; desktop fresh-install gateway fix; public-source hygiene sweep.

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
