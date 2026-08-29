// ingest.go 是签名 feed 的摄取器：验签 → 解析 → 新鲜度 → 防回滚，
// 全过才推进基线并持久化 feed 原文（`ofd catalog report` 回读）。
// 基线（最后接受版本）持久化于 store meta 表，重启后旧版本/重放
// 依然被拒。任何失败保留旧目录状态——feed 是增强数据，永不阻断。
package catalogfeed

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/swsgbl/omnifusion/internal/store"
)

const (
	// FeedInterval 是 feed 定时刷新周期（与目录 live 同步同频）。
	FeedInterval = time.Hour
	// maxFeedBytes 是 feed 响应体上限（防滥用拉爆内存）。
	maxFeedBytes = 8 << 20
	// SignatureHeader 是随 feed 分发的 detached 签名响应头。
	SignatureHeader = "x-catalog-signature"
	// meta 基线键（防回滚基线 + 上次接受原文）。
	metaVersion  = "catalog_feed_version"
	metaFeedJSON = "catalog_feed_json"
)

// Ingestor 摄取签名 feed。零值不可用，经 NewIngestor 构造。
type Ingestor struct {
	st  *store.Store // nil = 不持久化（纯内存测试）
	pub ed25519.PublicKey
	log *slog.Logger
	now func() time.Time
	hc  *http.Client
}

// NewIngestor 构造摄取器：pub 为 pinned 公钥（配置解析后传入）。
func NewIngestor(st *store.Store, pub ed25519.PublicKey, log *slog.Logger) *Ingestor {
	return &Ingestor{
		st: st, pub: pub, log: log, now: time.Now,
		hc: &http.Client{Timeout: 15 * time.Second},
	}
}

// Baseline 返回当前基线版本（0 = 尚未接受过任何 feed）。
func (g *Ingestor) Baseline() int64 {
	if g.st == nil {
		return 0
	}
	v, err := g.st.GetMeta(metaVersion)
	if err != nil || v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// LastFeed 返回上次接受的 feed 原文（无则 nil）——report 回读用。
func (g *Ingestor) LastFeed() []byte {
	if g.st == nil {
		return nil
	}
	v, err := g.st.GetMeta(metaFeedJSON)
	if err != nil || v == "" {
		return nil
	}
	return []byte(v)
}

// Ingest 摄取一帧：验签 → 解析校验 → 新鲜度 → 防回滚，全过则推进
// 基线并持久化原文，返回 feed。版本不新于基线返回 *RollbackError。
func (g *Ingestor) Ingest(raw []byte, sigHex string) (*Feed, error) {
	if err := Verify(raw, sigHex, g.pub); err != nil {
		return nil, err
	}
	f, err := ParseFeed(raw)
	if err != nil {
		return nil, err
	}
	if err := f.CheckFreshness(g.now()); err != nil {
		return nil, err
	}
	if base := g.Baseline(); f.Version <= base {
		return nil, &RollbackError{FeedVersion: f.Version, Baseline: base}
	}
	if g.st != nil {
		if err := g.st.SetMeta(metaVersion, strconv.FormatInt(f.Version, 10)); err != nil {
			g.warnf("persist feed baseline failed", err)
		}
		if err := g.st.SetMeta(metaFeedJSON, string(raw)); err != nil {
			g.warnf("persist feed body failed", err)
		}
	}
	return f, nil
}

// Fetch 从 URL 拉取 feed 原始字节与 x-catalog-signature 签名头。
func (g *Ingestor) Fetch(ctx context.Context, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("catalogfeed: build request: %w", err)
	}
	resp, err := g.hc.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("catalogfeed: fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("catalogfeed: fetch: status %d", resp.StatusCode)
	}
	sig := strings.TrimSpace(resp.Header.Get(SignatureHeader))
	if sig == "" {
		return nil, "", fmt.Errorf("catalogfeed: fetch: missing %s header", SignatureHeader)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes))
	if err != nil {
		return nil, "", fmt.Errorf("catalogfeed: fetch: read body: %w", err)
	}
	return raw, sig, nil
}

// Refresh 拉取 + 摄取（serve 周期循环用）。
func (g *Ingestor) Refresh(ctx context.Context, url string) (*Feed, error) {
	raw, sig, err := g.Fetch(ctx, url)
	if err != nil {
		return nil, err
	}
	return g.Ingest(raw, sig)
}

func (g *Ingestor) warnf(msg string, err error) {
	if g.log != nil {
		g.log.Warn(msg, "err", err)
	}
}
