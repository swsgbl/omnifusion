// Package catalogfeed 承载 M6.5 签名目录 feed：社区维护的模型目录
// 补充数据（上下文窗口/免费层说明/众测状态）经 Ed25519 验签 + 版本
// 防回滚后供网关采用（学 FreeLLMAPI catalog-sync：pinned 公钥对原始
// 字节验签、失败即丢弃、防回滚基线）。签名是 detached 的（对 feed
// 原始字节签名，经 x-catalog-signature 响应头或 .sig 文件分发），
// 公钥在配置中 pin 死；版本号严格递增才接受，基线持久化于 store
// meta 表（重启仍拒旧版本/同版本重放）。
package catalogfeed

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 众测状态（社区众测协议 v1）：新条目以 probation
// 进 feed；网关侧真实流量证据（request_log 聚合，`ofd catalog report`）
// 供维护者裁决升降级——升 stable / 移出 feed 都靠发新版本 feed。
const (
	StatusStable    = "stable"
	StatusProbation = "probation"
)

// MaxClockSkew 是 generated_at 的未来容差（NTP 偏差容忍）。
const MaxClockSkew = 5 * time.Minute

// 摄取链的哨兵错误。
var (
	// ErrBadSignature 验签失败（坏签名/坏公钥/坏签名编码）。
	ErrBadSignature = errors.New("catalogfeed: signature verification failed")
)

// RollbackError 是防回滚拒绝：feed 版本不新于基线。FeedVersion ==
// Baseline 为同版本重放（周期轮询幂等跳过），小于则为真回滚——
// 两种都拒绝，日志级别由调用方区分。
type RollbackError struct {
	FeedVersion int64
	Baseline    int64
}

func (e *RollbackError) Error() string {
	return fmt.Sprintf("catalogfeed: feed version %d not newer than baseline %d (rollback/replay)",
		e.FeedVersion, e.Baseline)
}

// ModelEntry 是 feed 中一个模型条目。Capability 是社区维护的能力分
//（0-100，quality 策略的排序依据；0=未评级，排序时视为最弱并保持
// 注册序）。数据源建议 LMArena Elo 或维护者分级，随 feed 版本更新。
// PriceIn/PriceOut 是社区维护定价（USD / 1M tokens；显式 0 = 免费，
// 省略 = 不置评——与注册表同样的指针语义，两者必须成对声明）。
type ModelEntry struct {
	ID         string   `json:"id"`
	CtxLen     int64    `json:"ctx_len"`
	Status     string   `json:"status"`
	Capability float64  `json:"capability,omitempty"`
	PriceIn    *float64 `json:"price_in,omitempty"`
	PriceOut   *float64 `json:"price_out,omitempty"`
}

// ProviderFeed 是 feed 中一个 provider 的条目组。
type ProviderFeed struct {
	FreeTier string       `json:"free_tier,omitempty"`
	Models   []ModelEntry `json:"models"`
}

// Feed 是签名 feed 的文档结构。Version 单调递增（防回滚轴），
// GeneratedAt 为维护者生成时刻（unix 秒，拒未来时间戳）。
type Feed struct {
	Version     int64                   `json:"version"`
	GeneratedAt int64                   `json:"generated_at"`
	Providers   map[string]ProviderFeed `json:"providers"`
}

// ParseFeed 严格解析并校验结构——feed 是外部输入，宁拒勿烂。
func ParseFeed(raw []byte) (*Feed, error) {
	var f Feed
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("catalogfeed: parse JSON: %w", err)
	}
	if f.Version <= 0 {
		return nil, fmt.Errorf("catalogfeed: version must be positive, got %d", f.Version)
	}
	if f.GeneratedAt <= 0 {
		return nil, fmt.Errorf("catalogfeed: generated_at must be positive")
	}
	if len(f.Providers) == 0 {
		return nil, fmt.Errorf("catalogfeed: providers must not be empty")
	}
	for name, pf := range f.Providers {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("catalogfeed: empty provider name")
		}
		if len(pf.Models) == 0 {
			return nil, fmt.Errorf("catalogfeed: provider %q has no models", name)
		}
		for _, m := range pf.Models {
			if m.ID == "" {
				return nil, fmt.Errorf("catalogfeed: provider %q has empty model id", name)
			}
			if m.CtxLen < 0 {
				return nil, fmt.Errorf("catalogfeed: %s/%s has negative ctx_len", name, m.ID)
			}
			if m.Status != StatusStable && m.Status != StatusProbation {
				return nil, fmt.Errorf("catalogfeed: %s/%s has bad status %q", name, m.ID, m.Status)
			}
			if m.Capability < 0 || m.Capability > 100 {
				return nil, fmt.Errorf("catalogfeed: %s/%s has capability %v out of [0,100]", name, m.ID, m.Capability)
			}
			if (m.PriceIn == nil) != (m.PriceOut == nil) {
				return nil, fmt.Errorf("catalogfeed: %s/%s: price_in/price_out must be declared together", name, m.ID)
			}
			if m.PriceIn != nil && (*m.PriceIn < 0 || *m.PriceOut < 0) {
				return nil, fmt.Errorf("catalogfeed: %s/%s has negative price", name, m.ID)
			}
		}
	}
	return &f, nil
}

// CheckFreshness 拒绝生成时刻在未来的 feed（时钟攻击/伪造新鲜度）。
func (f *Feed) CheckFreshness(now time.Time) error {
	if f.GeneratedAt > now.Add(MaxClockSkew).Unix() {
		return fmt.Errorf("catalogfeed: generated_at %d is in the future (now %d)",
			f.GeneratedAt, now.Unix())
	}
	return nil
}

// ParsePublicKey 解析 pinned 公钥（ed25519，64 hex）。
func ParsePublicKey(pubHex string) (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(strings.TrimSpace(pubHex))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("catalogfeed: bad public key (want %d hex chars)",
			ed25519.PublicKeySize*2)
	}
	return ed25519.PublicKey(b), nil
}

// ParseSignature 解析 detached 签名（128 hex）。
func ParseSignature(sigHex string) ([]byte, error) {
	b, err := hex.DecodeString(strings.TrimSpace(sigHex))
	if err != nil || len(b) != ed25519.SignatureSize {
		return nil, fmt.Errorf("catalogfeed: bad signature (want %d hex chars)",
			ed25519.SignatureSize*2)
	}
	return b, nil
}

// Verify 对 feed 原始字节验签（pinned 公钥）。
func Verify(raw []byte, sigHex string, pub ed25519.PublicKey) error {
	sig, err := ParseSignature(sigHex)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, raw, sig) {
		return ErrBadSignature
	}
	return nil
}

// Sign 用 hex seed（64 hex）私钥对原始字节签名，返回签名 hex 与
// 对应公钥 hex（维护者工具与测试共用）。
func Sign(raw []byte, seedHex string) (sigHex, pubHex string, err error) {
	seed, err := hex.DecodeString(strings.TrimSpace(seedHex))
	if err != nil || len(seed) != ed25519.SeedSize {
		return "", "", fmt.Errorf("catalogfeed: bad seed (want %d hex chars)", ed25519.SeedSize*2)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	sig := ed25519.Sign(priv, raw)
	return hex.EncodeToString(sig), hex.EncodeToString(priv.Public().(ed25519.PublicKey)), nil
}

// GenerateKey 生成新 ed25519 密钥对，返回 seed hex（私钥，妥存）与
// 公钥 hex（pin 进配置）。
func GenerateKey() (seedHex, pubHex string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("catalogfeed: generate key: %w", err)
	}
	return hex.EncodeToString(priv.Seed()), hex.EncodeToString(pub), nil
}
