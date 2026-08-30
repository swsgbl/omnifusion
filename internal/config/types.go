package config

// 本文件集中存放各配置段的类型声明（combos/fusion/semantic/mlrouter/
// catalog/guardrails/metrics/audit/a2a/server/store/log），加载与校验
// 逻辑分别在 config.go 与 validate.go。

// A2AConfig 控制 Agent2Agent v1.0 协议面：发现端点
// /.well-known/agent-card.json + JSON-RPC /rpc（均挂网关 key 之外/之内，
// 数据面同令牌）。默认开启——与 /v1/** 同鉴权强度，无新增暴露面。
type A2AConfig struct {
	// Enabled 关闭即不注册两路由。
	Enabled bool `yaml:"enabled"`
	// Name/Description 覆写 AgentCard 展示字段（可空 = 内置文案）。
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	// DefaultModel 是 message.metadata.model 缺席时的目标模型，
	// 可含网关指令（@smart/@fusion/@combo:NAME/裸模型名）。
	DefaultModel string `yaml:"default_model"`
}

// CatalogConfig 是 签名目录 feed：社区维护的窗口/众测数据源。
// feed_url + feed_pubkey 成对配置才启用（pinned Ed25519 公钥，64 hex）；
// 双空 = 未启用（默认，零行为变更）。
type CatalogConfig struct {
	FeedURL    string `yaml:"feed_url"`
	FeedPubkey string `yaml:"feed_pubkey"`
}

// CombosConfig 是命名组合层：路由组合（命名模型组）+ 压缩
// 组合（有序阶段名），路由组合可绑定一个压缩组合实现 per-path
// 压缩策略（免费层路径用激进压缩）。
type CombosConfig struct {
	// Routing 是路由组合：model 内嵌 "@combo:NAME" 选择。
	Routing map[string]RoutingComboConfig `yaml:"routing"`
	// Compression 是压缩组合：阶段名有序列表（dedup/toolfilter/caveman）。
	Compression map[string][]string `yaml:"compression"`
}

// RoutingComboConfig 是一个路由组合的声明。
type RoutingComboConfig struct {
	// Members 是有序成员（声明序即尝试优先级）。
	Members []ComboMemberConfig `yaml:"members"`
	// Compression 是绑定的压缩组合名（可空 = 纯路由组合）。
	Compression string `yaml:"compression"`
}

// ComboMemberConfig 是组合成员：provider 名 + 发往该上游的模型名。
type ComboMemberConfig struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
}

// FusionConfig 是 Fusion 扇出组（model 内嵌 "@fusion" 指令选择）：
// 并行扇出异构成员 → QUORUM 门控 → 轻量 Judge 合成。空 members =
// 未启用（@fusion 请求在边界 400）。
type FusionConfig struct {
	// Members 是扇出成员（建议 3–5 个异构模型）。
	Members []ComboMemberConfig `yaml:"members"`
	// Judge 是合成 Judge；缺省用 Members[0]。
	Judge *ComboMemberConfig `yaml:"judge,omitempty"`
	// Quorum 是门控阈值（成功扇出数下限，Judge 合成的前置条件）；
	// 0 = 默认 2。低于门控但 ≥1 成功时降级直通该成员响应。
	Quorum int `yaml:"quorum"`
}

// SemanticConfig 是 语义压缩段：零值 = 不附加配置（规则档
// semantic 阶段用内置默认参数，sidecar 档不可用）。配置 sidecar_url
// 后可选档（semantic_sidecar 阶段）生效——LLMLingua-2 级神经压缩
// 以 sidecar HTTP 服务进程外部署（学习型模型不进默认二进制），默认二进制零模型依赖。
type SemanticConfig struct {
	// SidecarURL 是 sidecar 压缩服务地址（POST {texts,rate}→{texts}）。
	SidecarURL string `yaml:"sidecar_url"`
	// Rate 是句保留率（0.1–0.9；0 = 默认 0.5）。
	Rate float64 `yaml:"rate"`
}

// MLRouterConfig 是 ML 路由段（model 内嵌 "@smart" 指令选择）：
// 弱/强两档成员 + 难度阈值。双双为空 = 未启用；只配一边 fail-fast。
// 难度 ≥ 阈值走强档，否则弱档；另一档殿后作 failover。默认纯 Go
// 启发式分类（ONNX 对比项走可选构建，不进默认二进制）。
type MLRouterConfig struct {
	// Weak 是弱档目标（小/免费模型，承接简单请求）。
	Weak *ComboMemberConfig `yaml:"weak"`
	// Strong 是强档目标（大模型，承接高难度请求）。
	Strong *ComboMemberConfig `yaml:"strong"`
	// Threshold 是难度分档阈值（0 = 默认 0.55；合法区间 (0,1)）。
	Threshold float64 `yaml:"threshold"`
}

// ServerConfig 控制 HTTP 监听。
// Host 默认 127.0.0.1 是安全红线（ R5）：改绑非回环地址必须显式配置。
type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

// StoreConfig 控制 SQLite 落盘位置。
type StoreConfig struct {
	Path string `yaml:"path"`
}

// LogConfig 控制结构化日志。
type LogConfig struct {
	Level  string `yaml:"level"`  // debug|info|warn|error
	Format string `yaml:"format"` // json|text
}

// GuardrailsConfig 是规则型护栏：默认关闭（BYOK 个人网关不意外
// 拦截），显式 enabled: true 启用；处置与规则名合法性在装配期由
// security.NewGuardrails 校验（fail-fast）。
type GuardrailsConfig struct {
	Enabled   bool                   `yaml:"enabled"`
	PII       GuardrailsPIIConfig    `yaml:"pii"`
	Injection GuardrailsActionConfig `yaml:"injection"`
}

// GuardrailsPIIConfig 控制 PII 检测：action 默认 block；
// types 是选用的规则名（email/phone_cn/cn_id/bank_card/secret_key），
// 缺省全部。
type GuardrailsPIIConfig struct {
	Action string   `yaml:"action"` // block|warn|off
	Types  []string `yaml:"types"`
}

// GuardrailsActionConfig 控制注入模式检测：action 默认 warn（告警放行）。
type GuardrailsActionConfig struct {
	Action string `yaml:"action"` // warn|block|off
}

// MetricsConfig 控制 /metrics 暴露：默认开启——被动观测不改变
// 请求行为，端点挂网关 key 鉴权（与数据面同令牌），不新开匿名信息面。
type MetricsConfig struct {
	Enabled bool `yaml:"enabled"`
}

// AuditConfig 控制请求审计日志：默认开启，每请求一行
// （含护栏拦截的 400），max_rows 上限之外自动裁最旧。
type AuditConfig struct {
	Enabled bool `yaml:"enabled"`
	MaxRows int  `yaml:"max_rows"`
}
