# 更新日志（Changelog）

本文件记录用户可感知的变更；格式遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号语义化（SemVer）。

## [v0.1.21] - 2026-09-30

升级蓝图第三批施工交付（Phase 5~8，七个垂直切片）：结构感知压缩、Fidelity 证据、MCP Tasks 与可路由标头、A2A 持久任务面、密钥脱敏。
> EN: Third blueprint delivery batch (Phases 5-8, seven vertical slices): structure-aware compression, fidelity evidence, MCP Tasks + routable headers, durable A2A task plane, secret redaction.

### 新增

- **结构感知折叠阶段**（`internal/compression/folding.go`，蓝图 Phase 5）：JSON 走 `json.Compact` 纯空白消除（字节无损）、围栏代码块行级空白规整（缩进零损伤）；解析失败或压不短原样保留；combo 配置名 `"folding"` 可引用；
- **structured_integrity 保真门规则**：盲文本压缩阶段（caveman/semantic/截断）碰到 JSON/代码块一律 fail-closed 拦下回退——蓝图硬规则"结构化内容优先结构感知压缩"由门强制；dedup 2→1 合法折叠放行；
- **FidelityReport 任务级证据**（蓝图 Phase 5）：`RunWithReport` 逐阶段输出结构化值覆盖率、工具参数一致性、system 完整性、压缩比、失败类型（命中规则名），单行摘要进网关日志——gate 不止 pass/fail；
- **Tasks 扩展状态核心**（`internal/agent/tasks.go`，蓝图 Phase 6）：六态生命周期 + 迁移白名单 + 幂等（终态同载荷重放 no-op、幂等键活跃去重）+ 取消 + 惰性超时 + 重启恢复（僵尸 running 诚实判 failed）；MCP 工具 `omnifusion_task_get/update/cancel` 挂**新第六 scope `tasks`**；
- **任务持久化**（store v10/v11 迁移）：任务重启存活——completed 结果与幂等键原样、篡改被拒；A2A contextId 持久列；
- **MCP 可路由标头 + 工具目录指纹**（蓝图 Phase 6）：`Mcp-Method`/`Mcp-Name`/`MCP-Protocol-Version` 进结构化日志（协议版本显式维度）；响应盖 `X-OmniFusion-Tool-Catalog` 指纹（真实进程内会话枚举，零漂移）——代理免解析体检测目录漂移；标头与请求体矛盾告警不拒；
- **A2A 持久任务面**（蓝图 Phase 7）：流任务落 TaskStore——**GetTask / CancelTask / ListTasks / SubscribeToTask** 生效；CancelTask 可中途打断活流（取消上下文贯通上游）；重启后任务可查；
- **密钥形态脱敏**（`internal/security/redact.go`，蓝图 Phase 8）：上游错误体回显密钥的泄露通道封堵——日志与客户端错误面只见 `[REDACTED]`（Bearer/键值上下文保留，标准密钥前缀全表覆盖）。

### 安全

- 三条利用回归测试（蓝图 §九）：恶意上游密钥回显→双面零泄露；scoped token 打数据面 401（跨权限缓存面不存在）；user 字段缓存隔离（不同用户互不命中）。

### 工程说明

- 全部加法式变更（旧 API/行为零破坏；A2A 未装配任务面时保持 transient 兼容语义）；
- 依赖纪律：TaskStore 持久化经 Persister 钩子 + cmd/ofd 适配器（store 叶子不 import agent）；
- 测试证据：compression folding/gate/report 契约、agent 状态机/持久化重启存活、A2A 九测（含取消打断 e2e）、security 脱敏 + 三条利用回归——全 suite + vet 无回归。

## [v0.1.20] - 2026-09-30

升级蓝图第二批施工交付（Phase 4 Cache 2.0，三个垂直切片）：缓存策略门、目录代际失效、缓存键证据头。
> EN: Second blueprint delivery batch (Phase 4 Cache 2.0, three vertical slices): cache policy gate, catalog-generation invalidation, cache-key evidence header.

### 新增

- **缓存策略门**（`internal/intelligence/cachepolicy.go`，蓝图 Phase 4——"工具调用/动态内容永不无条件语义缓存"红线落地）：
  - 请求携带 tools → 查询与回写双向 **BYPASS**（硬规则，seed 不可豁免——工具 schema 变化语义不可判定）；
  - 上下文含 tool 角色消息或 assistant `tool_calls` → BYPASS（工具结果参与语义等价判定不可靠）；
  - 响应含 `tool_calls` / finish_reason=tool_calls → 回写 BYPASS；
  - temperature≠0 且无 seed → BYPASS；**seed=opt-in 确定性，temperature=0=确定性采样**，二者任一才可入缓存；
  - 五个结论码（ok / bypass_tool_schema / bypass_tool_result / bypass_response_tool_call / bypass_non_deterministic）全部带契约测试；
- **目录代际失效**（Cache 2.0 防投毒/防陈旧）：Provider 目录每次实际变更递增 Generation；缓存键载荷嵌入 Generation → 目录变了键自然变，旧条目无锁过期、零清理扫描；`CacheKey(req)` 保持 generation-0 兼容语义；
- **缓存键证据头** `X-OmniFusion-Cache-Key`（前 12 位 hex）：OpenAI chat 端点命中/未命中两路均返回——排障时一眼对齐"为什么命中/为什么不命中"，与 `X-OmniFusion-Cache` (hit/miss) 和 `X-OmniFusion-Route` 组成完整证据链；
- **缓存测试夹具收紧**：确定性前提显式化（temperature=0），策略门行为变化被既有 hit/cross-protocol 用例即时捕获。

### 工程说明

- 全部加法式变更；策略门在 Lookup/WriteBack 入口短路，未装配（nil cache）路径零影响；
- 跨协议缓存共享（Anthropic 首问 → OpenAI 同逻辑请求命中）经审计确认为 IR 层正确特性，保留并加测试锁死；
- 测试证据：`internal/intelligence` 策略门全表用例 + `internal/server` hit/miss/cross-protocol 回归 + 目录代际变化→键变化用例，全 suite + vet 无回归。

## [v0.1.19] - 2026-09-25

升级蓝图第一批施工交付（Phase 1~3，九个垂直切片）：协议内核、可回放路由决策、权益账本。
> EN: First blueprint delivery batch (Phases 1-3, nine vertical slices): protocol core, replayable route decisions, entitlement ledger.

### 新增

- **协议内核**（`internal/protocol`）：NormalizedRequest/Response、ToolCall/Result、ErrorClass+显式 Retryability（never/cooldown/failover——重试语义不再由调用方按状态码猜）、CapabilityMatrix；契约测试锁死 JSON 形状与分类全表；
- **可回放路由决策**（蓝图 Phase 2 门禁）：`RouteDecision` 对象——候选全集+逐候选结果分类+选择原因码（FIRST_SUCCESS/FAILOVER_CHOSEN/CIRCUIT_OPEN…）+耗时，可 JSON 序列化落 audit/replay；`Attempt.SkipReason` 结构化（被熔断/冷却/配额跳过的候选带着原因进证据）；`FoldDecision` 供既有 Dispatch 调用点零签名接入；
- **四协议端点决策证据全覆盖**（OpenAI chat / Anthropic messages / Gemini / Responses 非流式路径）：响应头 **`X-OmniFusion-Route`**（单行摘要：选了谁、为什么、试了几次、完整候选路径）+ debug 级结构化日志回放；候选集来源精确标注（@smart/@quality/@cheap/combo/直连）；
- **权益账本**（`internal/quota`，蓝图 Phase 3——"免费额度"从静态声明升级为可验证事实）：
  - 五态模型：UNKNOWN / VERIFIED_FREE / VERIFIED_PAID / EXPIRED / DISABLED——UNKNOWN 永不伪装成 FREE 或余量 0；
  - 六源证据链按优先级合并：人工 > 运行时429 > 运行时用量 > 主动探测 > 官方文档 > 静态声明；
  - **时效降级**：过 ValidUntil 的证据输给任何新鲜观测；读时自动降级 EXPIRED 并持久化；
  - **429 = 窗口耗尽而非权益消失**（state 仍 FREE、余量 0）；402 = VERIFIED_PAID（free 层没了）；
  - 每条 VERIFIED_* 结论必须带 EvidenceID；密钥零明文；
- **分发循环观测闭环**：Router 装配 Ledger 后，429 配额类/402 自动喂入账本；普通限流 429 与成功不喂（限流≠配额事实，成功不区分免费付费）；
- **dashboard 免费层列**（providers 页）：五态徽标+余量百分比+证据溯源行（"观测于 14:32 · 运行时 429"）——用户看到的是"为什么这么分类"，不是黑盒；
- **SQLite 持久化**（v9 迁移）：权益事实重启不丢——恢复先于静态种子（运行时证据优先级高不被降级）；被合并规则拒绝的证据不落库；落库失败不阻断内存路径。**实测门禁场景：运行时 402 → 重启 → 仍 VERIFIED_PAID 带原证据**；
- **生产装配**：buildRouter 无条件注入 Ledger（空注册表也带）+ 注册表 rate_limits 声明作为静态种子入账（8 家）。

### 工程说明

- 全部加法式变更（既有 API/行为零破坏）；9 片全部带契约测试，全 suite + vet 无回归；
- 依赖纪律：quota 包保持叶子（store 实现 Persister 接口，quota 不 import store）；
- 蓝图施工文档（2026-09-29 三文档）驱动；Phase 0 基线审计见 `docs-internal/audit/`（VERIFIED/INFERRED/UNKNOWN/RISK 四级分类）。

## [v0.1.18] - 2026-09-25

回滚 v0.1.17 壳逻辑回归（看门狗/重建 additions 会卡死壳轮询），保留全部内核修复。**用户实测：点启动网关 → 管家页面正常渲染。**
> EN: Reverts the v0.1.17 shell-logic regression (the watchdog/recreate additions could wedge the shell's polling loop) while keeping all kernel-side fixes. **User-verified: clicking Start Gateway renders the butler page.**

### 修复

- **仪表页不显示的最终定位**：二分实验实锤——v0.1.17 起新增的壳 JS（IPC 看门狗的 not-found 计数/页面加载心跳/轮询内重建 webview）会在部分机器上卡死壳的轮询线程（表现为徽标冻结、内嵌页永不加载——恰是它要修的症状）。壳逻辑回退到 v0.1.16 的已验证形态；
- **保留的内核侧修复**（全部经真机验证）：点击穿透样式自愈（纯 Win32 枚举，注入 1 秒剥掉）、WebView2 缓存仅在版本变化时清除（每次启动都删会在上一实例 WebView2 进程未退时损坏新会话的 ipc:// 协议——新装即死的真因）、子 webview 导航改 eval 自导航（宿主侧 Navigate 会毒化 IPC 分发）、显示子 webview 不再立即 MoveFocus；
- 内嵌页加载心跳（cookie）保留作诊断数据。

## [v0.1.17] - 2026-09-25

仪表页死界面修复：IPC 看门狗 + 移除显示即 MoveFocus。
> EN: Dead-dashboard fix - IPC watchdog, no more MoveFocus on show.

### 修复

- **对话/提供商/密钥/用量/压缩/弹性页不显示（设置/关于正常）**：用户侧活体取证——网关在跑（托管）、壳每 3 秒都在探测（请求通路活着），但徽标冻结在"command not found"、内嵌页隐藏（**响应通路死亡**）。死亡发生在应用内启动网关成功之后（停→起转换）。两个针对性修复：显示子 webview 后**不再立即 MoveFocus**（长时间隐藏的 webview 被 WebView2 挂起时，立即 MoveFocus 是毒化整条 IPC 响应通路的头号嫌疑——焦点改由导航后的延时补焦承担）；停→起的强制重导航从 0.4s/1.6s 推迟到 1.2s/2.6s（避开 WebView2 恢复窗口）；
- **IPC 看门狗（自愈保底）**：壳持续追踪状态轮询是否"结算"（成功或失败都算）；**12 秒内没有任何一轮结算（=响应通路被毒化的确切特征）就整页自愈重载**。无论通路被什么毒化，用户最多看到一次界面闪动，不再是死界面。

## [v0.1.16] - 2026-09-24

点击穿透自愈 + 无条件壳缓存清除 + 白屏恢复（第三轮，全链路真机验证通过）。
> EN: Click-through self-heal, unconditional shell-cache bust, white-screen recovery - third round, verified end to end.

### 修复

- **鼠标点击整窗无效（含"缩放卡死"体感）**：外部覆盖层工具会把运行中的主窗口挂上点击穿透样式（WS_EX_TRANSPARENT|LAYERED|NOACTIVATE——键盘正常、鼠标全部穿透，"点什么都没反应"）。应用现在每轮状态轮询自检本进程窗口并剥掉该样式（纯 Win32 枚举，实测注入后 1 秒内自愈）；
- **升级不生效的根治**：WebView2 对壳页面的 HTTP 缓存连 `--disable-http-cache` 都挡不住——现在**每次启动**在任何 webview 创建之前物理清除缓存目录，壳页面永远与 exe 同版；
- **网关启动后内嵌页白屏**：长时间隐藏的子 webview 被 WebView2 挂起，停→起转换时的导航可能被静默吞掉（日志证实页面一次都没被请求）。壳在显示后 0.4s/1.6s 补两轮强制导航；「刷新」按钮同时强制重载内嵌页（自愈手段）；
- **回归清除**：上一轮的"隐藏时把子 webview 缩成 1×1"方案会在启动时把主线程拖死（整个 IPC 假死）——已回滚，webview 处理保持 v0.1.15 形态。

### 验证

同机全链路真机测试通过：点击启动网关 → 页面渲染 → 点击输入框 → **键入文字落入输入框**；穿透样式注入后 1 秒内自愈。

## [v0.1.15] - 2026-09-24

升级不生效根因修复：WebView2 缓存旧壳页面；缩放卡死；停机态显示死页面。
> EN: Fixes upgrades not taking effect (WebView2 serving a stale cached shell), the resize hang, and dead cached pages shown while the gateway is stopped.

### 修复

- **升级不生效（本轮最大发现，此前多轮"修复没用"的真因）**：WebView2 会缓存 `tauri.localhost` 的壳页面——**装了新版，跑的还是旧版界面代码**（新 Rust 内核 + 旧 JS 的混合体：焦点修复从未真正加载，输入框自然一直打不了字）。现在主窗口以 `--disable-http-cache` 创建（保留 tauri 默认禁用组件），升级即生效；网关侧 dashboard 页面响应统一加 `Cache-Control: no-store`；
- **网关未运行时显示"死"的对话页**：停机态下子 webview 不再导航、不再显示（此前会加载 HTTP 缓存里的旧对话页——看着活着、实际连不上，点击输入框打字无反应正是这个假象）。**停机时的唯一真相是引导卡 + 大号「启动网关」按钮**；
- **拖拽缩放窗口卡死（程序未响应）**：两处主线程 COM 重入隐患清除——窗口激活处理器不再内联 MoveFocus（改为发事件给壳、延时在安全泵点执行）；布局上报防抖合并（拖拽期间从每秒数十次 invoke 降为约 8 次）。

### 工程

- 状态轮询里 `gwRunning` 更新先于导航判定；托盘标签刷新随状态变化在主线程执行。

## [v0.1.14] - 2026-09-21

第二轮桌面可靠性：输入焦点补完、托盘唤起与菜单增强。
> EN: Second desktop reliability round — completes the keyboard-focus fix and upgrades the tray.

### 修复

- **管家输入框打不了字（v0.1.13 修复不彻底，本轮补完）**：v0.1.13 修的是"显示时交焦点"与"轮询不再扰动焦点"，但**托管网关停→起会触发子 webview 重导航**——重导航把之前交进去的键盘焦点吃掉，且此后鼠标点击也拿不回来（实测：该状态下点击输入框打字仍无反应，进程内无任何窗口持有焦点）。现在 `dash_navigate` 在页面加载完成后补两次 MoveFocus；对话页加载完成即自动聚焦输入框（visibilitychange 恢复时同样），发送/清空后焦点回落输入框；
- **托盘「显示主窗口」点了没反应**：窗口在被收进托盘若处于最小化状态，`show()` 会以最小化姿态回来（看不见）；前台被其它程序占用时 `SetForegroundWindow` 被系统前台锁挡住。现在唤起链为 unminimize → show → set_focus → 置顶一拍（把窗口带到最前后取消置顶）。

### 新增

- **托盘右键菜单增强**（原先只有"显示主窗口/退出"两项）：新增**打开对话**（唤起窗口并切到对话页）、**启动网关/停止网关**（标签随实际状态动态切换——后台 5 秒探测健康，状态变化自动换标签）、**检查更新**（有新版直接开下载页，无新版状态栏提示已是最新）。网关启停与更新检查发给壳层执行，复用设置里的路径配置，不在 Rust 侧维护第二份逻辑。

### 工程

- 单实例唤起改用与托盘相同的 `reveal_main_window`（unminimize + 置顶一拍）。

## [v0.1.13] - 2026-09-21

桌面端可靠性轮：输入焦点、单实例、进程生命周期与模型目录刷新。
> EN: Desktop reliability round — keyboard focus, single instance, process lifecycle, and model catalog refresh.

### 修复

- **管家输入框打不了字（键盘焦点链路，双根因）**：
  - 根因一：状态轮询每 3 秒**无条件**重发子 webview 显示命令——wry 的 `show()` 每次都执行 `ShowWindow(SW_SHOW)` + `SetIsVisible(true)`，焦点被反复扰动，键盘进不了输入框（鼠标点击正常、键盘失灵正是这个特征）。现在显隐只在状态**真正变化**时下发；
  - 根因二：WebView2 多控制器布局下键盘焦点需要宿主主动 `MoveFocus` 交接——子 webview 显示后与主窗口重新激活（alt-tab/任务栏/托盘唤起）时都会把焦点交还给它；
  - 附带修复：子 webview 创建后不再强制显示（按最近已知网关状态应用），网关未运行时不再闪一帧空白页；
- **可以同时启动多个 OmniFusion Desktop**：二次启动不再开出新进程（多实例会各自拉网关抢 20130 端口、托盘出现多个图标），而是唤起已有主窗口（接入 `tauri-plugin-single-instance`）；
- **托盘「退出」留下孤儿网关进程**：退出时现在会停掉**本应用拉起的**网关子进程（外部/CLI 启动的实例不动）——此前孤儿 `ofd.exe` 一直占着端口，下次启动状态栏显示「运行中（外部启动）」且停止按钮不可用；
- **桌面端启动网关忽略安装目录的 config.yaml**：设置里配置文件留空时自动采用安装目录自带的 `config.yaml`——自定义 provider/私有接入点模型（如 ep-xxx）直接生效，不必手动填路径。

### 新增

- **模型目录：自动刷新已有，现在可见、可手动**：网关本就启动即同步 + 每 1 小时自动拉取各家 live 模型清单（校验和判变更才落库）——providers 页新增**「刷新模型」按钮**（`POST /dashboard/api/models/refresh`，后台同步、防重入）与**同步状态行**（"模型目录每小时自动刷新；上次同步 …"）。厂商上新模型最多 1 小时自动可见，想立刻看就点按钮。

### 工程

- providers 页修复一处残缺的 `</head></head>` 标记。

## [v0.1.12] - 2026-09-13

修一处 v0.1.11 引入的可用性回归：网关没启动时，用户找不到「启动网关」。
> EN: Fixes a usability regression from v0.1.11 — with the gateway stopped, users could not find the "Start Gateway" control.

### 修复

- **「启动网关」按钮找不到（回归）**：v0.1.11 把网关操作从顶部移进底部状态栏，而状态栏只有 28px 高、按钮 11.5px，首次启动的用户面对空白内容区无处下手（提示还写着"点右下"，右下却看不清）。现在**网关未运行时，整块内容区就是一个大号「启动网关」按钮**（闪电图标 + 「网关未运行」+ 一行说明 + 大按钮）——视线落点即动作，按钮就在提示旁边；
- **状态栏动作区加固**：状态栏 28px→32px、按钮字号与内边距加大；动作区 `flex: 0 0 auto` 永不参与收缩，状态文字改为可截断（`min-width: 0` + 省略号）——状态文字再长、窗口再窄，也不会把「启动网关/停止」挤出可视区；
- **原生子 webview 高度按状态栏顶边夹紧**：子 webview 是原生层、浮在壳 HTML 之上（CSS z-index 对它无效），其高度现在被夹在状态栏上沿以内，任何取整/DPI 偏差都不会再压住底部按钮；
- **文案同步**：网关未运行的提示不再写"点右下"，改为指向眼前的按钮（中英双语）。

### 新增

- **`scripts/i18n-check.js`**：双语字典一致性护栏——校验 zh/en 键集合完全一致（含 detail/tabs 嵌套表）、代码里每个 `t.<key>` 都有定义、侧边栏每个页面都有 tabs 文案。空白菜单那类回归从此由脚本拦下，不再靠肉眼。

### 工程

- 清理 CHANGELOG 中重复堆积的 `[Unreleased]` 段（其三行内容早已随 v0.1.8 发布，属于文档残留）。

## [v0.1.11] - 2026-09-12

界面全面重构：浏览器式布局（左菜单 + 标签页）。
> EN: Complete UI redesign — browser-style layout with a left sidebar and real tabs.

### 新增

- **浏览器式标签页布局**：左侧竖向侧边栏（可折叠，Ctrl+B）承载全部导航；点任意菜单项在顶部标签条打开一个标签页（可关闭、可多开、切换即切换视图），标签与已打开页面状态持久化，重启后恢复上次布局——与浏览器/VS Code 的心智模型一致；
- **设置与关于改为页签**：不再弹出浮层对话框，而是与其他菜单完全同款：左侧统一入口、标签页方式打开（设置内仍分「网关/密钥/客户端」三个面板，卡片式分组）；
- **状态栏**：底部 28px 常驻条——网关状态点（呼吸）+ 状态文字 + 主机端口 + 就近的操作按钮（刷新/启动/停止），操作不再占用顶部空间；
- **空态引导**：没有打开任何标签时显示产品标识 + 建议入口（对话/密钥/提供商），不再是空白区域；
- **键盘快捷键**：Ctrl+B 折叠侧边栏、Ctrl+W 关闭当前标签、Ctrl+, 打开设置；
- **视觉规范落地**（现代开发工具通用范式）：GitHub Dark 色板、6/8/12px 圆角梯度、13px UI 字号、发丝边框替代阴影、单一强调色（accent）+ 语义色仅用于状态、focus ring 可见。

### 修复

- 侧边栏/标签条浮层与子 webview 的遮挡问题在重构中一并消除（壳内页签与 webview 互斥显示，状态轮询尊重该状态）。

## [v0.1.10] - 2026-09-11

应用外壳成熟轮 + 管家智能化。
> EN: App chrome comes of age, and the butler learns to discover.

### 新增

- **桌面应用菜单架构**：主界面简化为状态徽标 + 启动/停止 + ☰ 菜单；设置收进弹窗三标签页（网关/密钥/客户端），新增「检查更新」（主动查网关更新快照，有新版自动打开下载页）与「关于」（版本/定位一句话/仓库链接/许可声明）——从"工程面板"到"给小白的应用"的关键一步；
- **管家执行全程可见**：工具轮不再黑箱——每个 LLM 等待期显示"🧠 思考中 (第 N 轮) + 实时秒表"，每次工具调用显示为对话流内的实时卡片（名称+参数+计时，完成变绿打勾+结果摘要，失败红杠+错误）；
- **管家能力发现（智能化而非程序化）**：面对任何任务先"发现本机有什么能做"，用 find_ai_tool + run_command 现场验证再组合执行——能力边界是本机一切可执行程序，不是固定集成清单。装了 ollama 的用户自然获得视觉能力，没装的用户得到诚实的替代方案；
- **国产模型 XML 工具调用兼容**：GLM/Qwen 等模型以 `<tool_call>` 文本格式输出的工具调用（而非 OpenAI 结构化 tool_calls）现在被正确解析执行——三种实测格式全部支持。

### 修复

- parseXmlToolCalls 函数体曾因编辑事故丢失（调用处在了、定义丢了）——每条消息报"not defined"，已修复并加三格式单测。

## [v0.1.9] - 2026-09-09

更新提醒与国内双源轮：无遥测的"拉取式"更新检查。
> EN: The update-notification round — zero-telemetry, pull-based update checks with a China-direct mirror.

### 新增

- **无遥测更新检查（拉取式）**：网关启动后与每 24 小时静默拉一次 Release 元数据——不发任何用户数据、不带认证、失败静默下个周期再试；发现新版本经对话页横幅、桌面端徽标、`ofd status` 末行三处提示，**升级永远由用户在浏览器确认**（零遥测红线 + 持密钥进程不后台静默替换二进制）；
- **国内双源（GitCode 镜像）**：更新元数据与下载分发走 https://gitcode.com/hongfu/omnifusion（国内直连），GitHub 兜底——被墙环境更新检查不依赖代理；仓库同步镜像（main + 全部 tags）；
- **`ofd status` 版本对照行**：本地/远端版本不一致时末行显示 `update: v0.1.9 available (this: v0.1.8) — 下载页链接`，查询失败静默。

### 度量口径（工程说明）

使用量观测全部走平台原生数据，零自建遥测：Release 下载计数（分版本资产）、仓库克隆/访客数（GitHub Traffic API）、feed 拉取侧服务端日志（活跃估计）。

## [v0.1.8] - 2026-09-09

管家工程吸收轮：长任务、结构化提问、会话回滚。
> EN: The butler-engineering round — long-running commands, structured questions, persistent sessions with rollback.

### 新增

- **run_command 长任务能力**：`background=true` 后台启动服务/构建/观察类命令（单槽，返回任务 id），`collect_output` 拉取新增输出、`kill=true` 一击打断；前台命令 `timeout_sec` 5-300 秒可声明；输出超长时按 `head_lines`/`tail_lines` 保头保尾（错误通常在末尾）——替代 16KB 一刀切；Windows 命令输出 GBK 解码沿用。**实测**：`ping -t` 起后台→中文输出可读→一击必杀→进程消失；
- **ask_user 结构化提问**：管家遇到真需要用户裁决的分叉（选哪家厂商/走哪个方案）时，弹 2-4 个带说明的选项气泡 + 永远在位的"其他"自由输入，而非自由文本反问。模型实测：主动给出合规选项、按所选执行、自纠 `ping -c`→`-n` 平台差异；
- **对话会话持久化与回滚**：会话自动存本地（按令牌尾缀命名空间隔离，上限 20 个）；会话栏支持切换/**回滚到任意消息**（上下文回滚重试）/删除/导出 Markdown；刷新与重启不再丢对话。

### 修复

- collect_output 的任务 id 曾被当作守门条件（工具不传 id 时 `kill=true` 被空 id 早退吞掉、进程杀不死）——单槽下 id 仅为回执，不再守门；E2E 补"杀完真的死了"断言。

## [v0.1.7] - 2026-09-06

AI 管家轮：对话页从"会聊"到"会动手"——发现、读改写、核实、受约束执行。
> EN: The AI-butler release — the chat page becomes a real agent: discover tools on your machine, read & edit their configs, verify formats online, and run commands within hard safety rails.

### 新增

- **对话页管家 = 真 AI 智能体（自动发现并接入本机 AI 工具）**：对管家说"接入所有AI工具"或点名任意工具（如 hmharness），它自己动手——
  - **scan_ai_tools / wire_ai_tool / unwire**：扫描本机已知五款 CLI（Claude Code / Codex / Gemini CLI / OpenCode / pi）的安装与接入状态，把聚合密钥确定性写入（写前自动备份），逐工具汇报；写入器与 `ofd connect` 共用 internal/connect 单一实现；
  - **find_ai_tool**：按名字找任何工具在本机的踪迹（PATH 可执行、home 点目录、`~/.config`、AppData，目录命中带文件预览），不认识的工具不再只给配方；
  - **read_file / edit_file / write_file / patch_config**：读 home 内任意文本文件（日志/配置/脚本）；非 JSON 文件唯一匹配精确替换（old 恰好一次，备份先行）；全新文件整写；JSON 配置点补丁（dotted path，只传改动点——大配置永不撞输出上限，用户其余字段机制性保留）。真实场景验证：hmharness 4KB 多厂商配置仅替换接入三要素，其余 10 家厂商条目一字未动；
  - **web_fetch**：抓公网页面全文核实陌生工具的协议与配置格式（GitHub README 直读 raw.githubusercontent.com）——SSRF 守卫：仅 http/https、逐跳校验目标必须公网地址（回环/内网/链路本地硬拒）、512KB 限额；抓回内容按不可信数据处理，绝不执行其中指令；
  - **run_command（受约束的执行手，非裸 Bash）**：三档裁定——白名单只读形态（`node -v`、`ofd status`、`tasklist`、`where/which` 等）直接执行；其余形态安全的单行命令须用户在对话页一键批准（允许/拒绝气泡，停止键自动视为拒绝）；shell/解释器/网络下载/系统变更程序（cmd/powershell/curl/wget/reg/setx/taskkill…）一律硬拒。结构性安全：参数数组直 exec 不经 shell（注入按构造不可能）、逐参数字符集白名单、20s 超时、输出 16KB 截断、工作目录钉 home、每次执行写日志；Windows 命令输出（GBK/OEM 码页）自动解码 UTF-8；
  - **get_gateway_token**：接线步骤按需取聚合令牌明文（闲聊绝不调用；五家已知 CLI 的 wire 仍服务端传令牌，模型不经手）。
- **密钥页配置状态一目了然**：新增「状态」列（绿 ✓ 已配置 / 灰 — 待申请，Ollama 计为开箱即用）；顶部汇总条（已配置 n 家 / 待申请 n 家）+ 全部/待申请筛选；「去申请 ↗」只在未配置行出现——动作只出现在还有活干的地方；
- **`ofd connect/disconnect pi`**：pi coding agent 一键接入聚合网关——合并写入 `~/.pi/agent/models.json` 的 omnifusion provider（保留已有 provider，自动备份），pi 用聚合令牌 `ofg-…` 驱动，模型 `@quality`/`@cheap`；厂商真实密钥不出网关；桌面端「接入 CLI 客户端」下拉同步支持 pi；
- **运维 AI 助手提示词**：`prompts/ops-agent.md`——现成 system prompt（只读优先、不索厂商密钥、免费档优先），pi / Claude Code 挂 `ofd mcp` 即成为网关运维助手；
- **厂商名双语渲染**：dashboard 各表提供商列按语言显示「中文名 (id)」/「English name (id)」——id 括注保证与 CLI 命令对得上号。

### 修复

- **上游"200 + 空 choices"伪成功**：个别厂商偶发空响应曾被当成功并进语义缓存钉死 24 小时（同一请求持续命中空回复）——现归类为上游错误触发 failover，绝不入缓存；
- **管家长链路静默截断（对话"不回复"）**：工具循环输出上限被 4KB 配置重写撞爆，截断消息被当空回复推进历史、页面无显示——上限提升至 12000，截断/空回复改为可见可重试的双语提示，工具参数解析失败回喂模型绝不带空参执行，服务端拒绝写入空内容；
- **接入写入丢端口**：管家 API 生成的网关地址缺端口号（会打到 80）——与 CLI 共用 connect.Origin 统一归一。

### 维护

- 桌面安装器 PREINSTALL 钩子：静默升级自动退出运行中的旧程序（主程序整树 + 网关兜底），覆盖安装不再产出"版本混合"；
- 内嵌页两处加载级缺陷：motion.js 重复 script 标签移除；首帧 ReferenceError 根治（脚本入 head + 渲染函数降级守卫）；
- 仓库新增 `scripts/js-syntax-check.mjs`：dashboard 内联 JS 部署前语法门禁（本轮真拦截过一次字符串裸换行）。

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
