// Package security 是 L2：密钥保管与网关鉴权（ 目录）。
// 落地 keyring（AES-256-GCM 静态加密，R5 对策 3）。
package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"os"
	"os/user"
)

// cipherVersion 前缀标识密文格式，给未来换 KDF/算法留演进位。
const cipherVersion byte = 0x01

// hkdfSalt 固定域分离盐：主密钥只服务本项目密钥保管。
const hkdfSalt = "omnifusion/keyring/v1"

// Keyring 持有派生出的主密钥，提供 AES-256-GCM 加解密。
// 主密钥默认从机器身份派生（R5：本地单用户场景），可选 passphrase
// 增强；明文密钥只在调用方取用瞬间存在，不落盘、不进日志。
type Keyring struct {
	master []byte
}

// PassphraseEnv 是可选口令的环境变量名（R5 对策 3「可选 passphrase」
// 的接线点，第三轮审计补）：设置后主密钥额外混入口令，密文库与网关
// token 随之换域（换口令前存的密钥需重新录入）。所有调用点（serve 与
// 各子命令）都经 Open("") 同一入口，不存在半边换口令的派生不一致。
const PassphraseEnv = "OFD_KEYRING_PASSPHRASE"

// Open 派生主密钥并返回 Keyring。passphrase 为空时查 PassphraseEnv
// 环境变量（仍空 = 仅机器身份，与历史行为逐字节一致）。
func Open(passphrase string) (*Keyring, error) {
	if passphrase == "" {
		passphrase = os.Getenv(PassphraseEnv)
	}
	secret, err := machineSecret(passphrase)
	if err != nil {
		return nil, err
	}
	// AES-256 主密钥（RFC 5869 一步 Extract+Expand）
	master, err := hkdf.Key(sha256.New, secret, []byte(hkdfSalt), "master-key", 32)
	if err != nil {
		return nil, fmt.Errorf("keyring: derive master key: %w", err)
	}
	return &Keyring{master: master}, nil
}

// machineSecret 汇总机器身份（主机名 + 当前用户）与可选 passphrase。
// 身份变化（改名/换用户）会使既有密文不可解，届时需重新录入密钥。
func machineSecret(passphrase string) ([]byte, error) {
	host, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("keyring: hostname: %w", err)
	}
	name := "unknown"
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	secret := []byte(host + "\x00" + name)
	if passphrase != "" {
		secret = append(secret, "\x00"+passphrase...)
	}
	return secret, nil
}

// Encrypt 用 AES-256-GCM 加密，输出 版本前缀 || nonce || 密文+tag。
func (k *Keyring) Encrypt(plaintext []byte) ([]byte, error) {
	gcm, err := k.gcm()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("keyring: nonce: %w", err)
	}
	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+gcm.Overhead())
	out = append(out, cipherVersion)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, nil), nil
}

// Decrypt 解出明文；版本未知或认证失败均报错。
func (k *Keyring) Decrypt(data []byte) ([]byte, error) {
	if len(data) < 1 || data[0] != cipherVersion {
		return nil, fmt.Errorf("keyring: unknown cipher format")
	}
	gcm, err := k.gcm()
	if err != nil {
		return nil, err
	}
	body := data[1:]
	if len(body) < gcm.NonceSize() {
		return nil, fmt.Errorf("keyring: ciphertext too short")
	}
	nonce, ct := body[:gcm.NonceSize()], body[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("keyring: decrypt: %w", err)
	}
	return plain, nil
}

func (k *Keyring) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(k.master)
	if err != nil {
		return nil, fmt.Errorf("keyring: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("keyring: gcm: %w", err)
	}
	return gcm, nil
}
