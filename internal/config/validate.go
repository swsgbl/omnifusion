package config

import (
	"fmt"
	"net"
	"net/url"
)

// Validate 校验配置合法性。
func (c *Config) Validate() error {
	if ip := net.ParseIP(c.Server.Host); ip == nil {
		return fmt.Errorf("server.host: %q 不是合法 IP", c.Server.Host)
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port: %d 超出范围", c.Server.Port)
	}
	if c.Store.Path == "" {
		return fmt.Errorf("store.path 不能为空")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level: %q 不合法（debug|info|warn|error）", c.Log.Level)
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		return fmt.Errorf("log.format: %q 不合法（json|text）", c.Log.Format)
	}
	if c.Audit.MaxRows < 0 {
		return fmt.Errorf("audit.max_rows: %d 不能为负", c.Audit.MaxRows)
	}
	if err := c.validateCombos(); err != nil {
		return err
	}
	if err := c.validateFusion(); err != nil {
		return err
	}
	if err := c.validateSemantic(); err != nil {
		return err
	}
	if err := c.validateMLRouter(); err != nil {
		return err
	}
	return c.validateCatalog()
}

// validateCatalog 校验签名 feed 段：双空 = 未启用放行；
// 只配一边 fail-fast；url 限 http(s)；pubkey 限 64 hex（ed25519）。
func (c *Config) validateCatalog() error {
	fc := c.Catalog
	if fc.FeedURL == "" && fc.FeedPubkey == "" {
		return nil
	}
	if fc.FeedURL == "" || fc.FeedPubkey == "" {
		return fmt.Errorf("catalog: feed_url and feed_pubkey must be configured together")
	}
	u, err := url.Parse(fc.FeedURL)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("catalog: feed_url must be a valid http(s) URL")
	}
	if len(fc.FeedPubkey) != 64 || !isHex(fc.FeedPubkey) {
		return fmt.Errorf("catalog: feed_pubkey must be 64 hex chars (ed25519)")
	}
	return nil
}

// isHex 报告 s 是否全为十六进制字符。
func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// validateCombos 校验组合段：成员非空且字段齐全；绑定的压缩组合必须
// 已定义（阶段名合法性由 compression.BuildCombo 在装配期校验）。
func (c *Config) validateCombos() error {
	for name, rc := range c.Combos.Routing {
		if len(rc.Members) == 0 {
			return fmt.Errorf("combos.routing.%s: members 不能为空", name)
		}
		for i, m := range rc.Members {
			if m.Provider == "" || m.Model == "" {
				return fmt.Errorf("combos.routing.%s.members[%d]: provider 与 model 均不能为空", name, i)
			}
		}
		if rc.Compression != "" {
			if _, ok := c.Combos.Compression[rc.Compression]; !ok {
				return fmt.Errorf("combos.routing.%s: 绑定的压缩组合 %q 未定义", name, rc.Compression)
			}
		}
	}
	return nil
}

// validateFusion 校验 Fusion 段：空 members = 未启用，放行；
// 启用时成员字段齐全且 ≥2 个（扇出+门控才有意义）、quorum 落在
// [2, members] 区间、judge 字段齐全。
func (c *Config) validateFusion() error {
	f := c.Fusion
	if len(f.Members) == 0 {
		return nil
	}
	if len(f.Members) < 2 {
		return fmt.Errorf("fusion.members: 至少 2 个成员（扇出+QUORUM 门控才有意义）")
	}
	for i, m := range f.Members {
		if m.Provider == "" || m.Model == "" {
			return fmt.Errorf("fusion.members[%d]: provider 与 model 均不能为空", i)
		}
	}
	if f.Judge != nil && (f.Judge.Provider == "" || f.Judge.Model == "") {
		return fmt.Errorf("fusion.judge: provider 与 model 均不能为空")
	}
	q := f.Quorum
	if q == 0 {
		q = 2
	}
	if q < 2 || q > len(f.Members) {
		return fmt.Errorf("fusion.quorum: %d 超出范围 [2, %d]", q, len(f.Members))
	}
	return nil
}

// validateSemantic 校验语义压缩段：rate=0 合法（取默认 0.5），
// 其余值须落 (0,1]；sidecar_url 启用时必须是合法 http(s) URL。
// 更细的钳位（0.1–0.9）由 compression.ConfigureSemantic 完成——
// 装配期归一而非启动失败，配置面保持宽松。
func (c *Config) validateSemantic() error {
	sc := c.Semantic
	if sc.Rate < 0 || sc.Rate > 1 {
		return fmt.Errorf("semantic.rate: %v 超出范围 (0,1]", sc.Rate)
	}
	if sc.SidecarURL == "" {
		return nil
	}
	u, err := url.Parse(sc.SidecarURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("semantic.sidecar_url: %q 不是合法 http(s) URL", sc.SidecarURL)
	}
	return nil
}

// validateMLRouter 校验 ML 路由段：双双为空 = 未启用放行；
// 只配一边 fail-fast（弱/强二分缺一不可）；成员字段齐全；threshold
// 落 (0,1)（0 = 默认 0.55）。
func (c *Config) validateMLRouter() error {
	m := c.MLRouter
	if m.Weak == nil && m.Strong == nil {
		return nil
	}
	if m.Weak == nil || m.Strong == nil {
		return fmt.Errorf("mlrouter: weak 与 strong 必须同时配置")
	}
	if m.Weak.Provider == "" || m.Weak.Model == "" ||
		m.Strong.Provider == "" || m.Strong.Model == "" {
		return fmt.Errorf("mlrouter: weak/strong 的 provider 与 model 均不能为空")
	}
	if m.Threshold < 0 || m.Threshold >= 1 {
		return fmt.Errorf("mlrouter.threshold: %v 超出范围 (0,1)", m.Threshold)
	}
	return nil
}
