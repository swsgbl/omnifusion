// card.go 构造 A2A AgentCard（发现端点 GET /.well-known/agent-card.json）。
package a2a

// HTTPAuthSecurityScheme 是 v1.0 SecurityScheme oneof 的 HTTP 认证
// 分支（RFC 7235 scheme 名）。
type HTTPAuthSecurityScheme struct {
	Description string `json:"description,omitempty"`
	Scheme      string `json:"scheme"` // "bearer"
	BearerFmt   string `json:"bearerFormat,omitempty"`
}

// SecurityScheme 是 v1.0 oneof 判别联合（0.3 的 type/scheme 平铺已废）；
// 本网关仅 HTTP Bearer（复用网关 key，与数据面 /v1/** 同一凭据）。
type SecurityScheme struct {
	HTTPAuth *HTTPAuthSecurityScheme `json:"httpAuthSecurityScheme,omitempty"`
}

// StringList 是作用域列表容器（proto repeated list 字段）。
type StringList struct {
	List []string `json:"list,omitempty"`
}

// SecurityRequirement 声明一组「方案名 → 所需 scope」。
type SecurityRequirement struct {
	Schemes map[string]StringList `json:"schemes"`
}

// AgentInterface 声明一个可用协议端点（spec §4.4：url + 协议绑定 + 版本）。
type AgentInterface struct {
	URL             string `json:"url"`
	ProtocolBinding string `json:"protocolBinding"`
	ProtocolVersion string `json:"protocolVersion"`
}

// AgentProvider 标识服务提供方。
type AgentProvider struct {
	Organization string `json:"organization"`
	URL          string `json:"url,omitempty"`
}

// AgentCapabilities 声明可选能力。
type AgentCapabilities struct {
	Streaming         bool `json:"streaming"`
	PushNotifications bool `json:"pushNotifications,omitempty"`
}

// AgentSkill 是一项对外能力描述。
type AgentSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
	Examples    []string `json:"examples,omitempty"`
}

// AgentCard 是代理发现清单（camelCase 线上形态）。
type AgentCard struct {
	Name                 string                    `json:"name"`
	Description          string                    `json:"description"`
	Version              string                    `json:"version"`
	SupportedInterfaces  []AgentInterface          `json:"supportedInterfaces"`
	Provider             *AgentProvider            `json:"provider,omitempty"`
	Capabilities         AgentCapabilities         `json:"capabilities"`
	SecuritySchemes      map[string]SecurityScheme `json:"securitySchemes,omitempty"`
	SecurityRequirements []SecurityRequirement     `json:"securityRequirements,omitempty"`
	DefaultInputModes    []string                  `json:"defaultInputModes"`
	DefaultOutputModes   []string                  `json:"defaultOutputModes"`
	Skills               []AgentSkill              `json:"skills"`
}

// CardOptions 汇聚构造卡片所需的运行时信息。
type CardOptions struct {
	BaseURL      string // 对外可达基址，如 http://127.0.0.1:20130（不含 /rpc）
	Name         string
	Description  string
	Version      string // 网关构建版本
	DefaultModel string // 未指定 metadata.model 时的目标模型（可含 @指令）
	Streaming    bool
}

// BuildCard 构造本网关的 AgentCard：单一 chat 技能（模型经
// message.metadata.model 选择，支持 @smart/@fusion/@combo 网关指令），
// JSON-RPC 绑定挂 BaseURL+"/rpc"。
func BuildCard(o CardOptions) *AgentCard {
	desc := o.Description
	if desc == "" {
		desc = "OmniFusion BYOK AI gateway: routes chat to configured " +
			"providers with failover, compression and caching. " +
			"Select model via message.metadata.model (gateway directives " +
			"@smart/@fusion/@combo:NAME supported); default " + o.DefaultModel + "."
	}
	name := o.Name
	if name == "" {
		name = "OmniFusion Gateway"
	}
	return &AgentCard{
		Name:        name,
		Description: desc,
		Version:     o.Version,
		SupportedInterfaces: []AgentInterface{{
			URL:             o.BaseURL + "/rpc",
			ProtocolBinding: "JSONRPC",
			ProtocolVersion: ProtocolVersion,
		}},
		Provider: &AgentProvider{
			Organization: "OmniFusion",
			URL:          "https://github.com/swsgbl/omnifusion",
		},
		Capabilities: AgentCapabilities{Streaming: o.Streaming},
		SecuritySchemes: map[string]SecurityScheme{
			"gatewayKey": {HTTPAuth: &HTTPAuthSecurityScheme{
				Scheme:      "bearer",
				BearerFmt:   "OmniFusion gateway key (ofg-...)",
				Description: "OmniFusion gateway key (Authorization: Bearer ofg-...)",
			}},
		},
		SecurityRequirements: []SecurityRequirement{{Schemes: map[string]StringList{"gatewayKey": {}}}},
		DefaultInputModes:    []string{"text/plain"},
		DefaultOutputModes:   []string{"text/plain"},
		Skills: []AgentSkill{{
			ID:          "chat",
			Name:        "Chat",
			Description: "Route a chat message to the gateway model fleet (failover, compression, semantic cache). Set message.metadata.model to pick a model or gateway directive.",
			Tags:        []string{"chat", "llm", "gateway", "routing"},
			Examples:    []string{"Summarize the attached context in three bullets."},
		}},
	}
}
