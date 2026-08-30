package security

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// gatewayTokenPrefix 让网关 key 可识别、可审计（日志脱敏时也易辨认）。
const gatewayTokenPrefix = "ofg-"

// GatewayToken 从主密钥派生网关统一 API Key（ gatewaykey.go：
// 派生/校验）。确定性派生意味着不落盘、不漂移，信任模型与 keyring
// 一致；主密钥变（换机/换用户/加 passphrase）则 key 随之而变。
func (k *Keyring) GatewayToken() (string, error) {
	key, err := hkdf.Key(sha256.New, k.master, []byte(hkdfSalt), "gateway-token", 32)
	if err != nil {
		return "", fmt.Errorf("keyring: derive gateway token: %w", err)
	}
	return gatewayTokenPrefix + hex.EncodeToString(key), nil
}

// IsGatewayTokenShape 报告字符串是否像合法网关 key（用于客户端侧
// 快速校验，不作为服务端鉴权手段）。
func IsGatewayTokenShape(s string) bool {
	if len(s) != len(gatewayTokenPrefix)+64 {
		return false
	}
	if s[:len(gatewayTokenPrefix)] != gatewayTokenPrefix {
		return false
	}
	_, err := hex.DecodeString(s[len(gatewayTokenPrefix):])
	return err == nil
}
