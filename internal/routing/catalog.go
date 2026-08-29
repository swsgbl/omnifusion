// catalog.go 是 M3.5 模型目录（docs/04 §6 models 表 / docs/05 3.5）：
// 定时（默认 1h）从各 provider 拉取模型清单，按校验和判断变更，
// 变更才整组落 SQLite；ErrNotSupported 的 provider 回落注册表静态
// 清单。Ed25519 目录验签（FreeLLMAPI 式）在 M6 接入，本层只做
// 校验和（内容指纹）与快照语义。
package routing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/store"
)

// SyncInterval 是定时同步周期（docs/05 3.5 验收：免费模型列表 1h 刷新）。
const SyncInterval = time.Hour

// 冷启动补齐退避：首轮同步失败且静态回落也为空的 provider（如本地
// ollama 尚未就绪）按此间隔短退避重试，最多 coldStartRetries 轮——
// 否则该 provider 的模型要空等满一个 SyncInterval（1h）才对客户端可见。
const (
	coldStartRetries = 6
	coldStartBackoff = 10 * time.Second
)

// CatalogEntry 是目录快照的平铺视图（/v1/models 与观测消费）。
type CatalogEntry struct {
	Provider string
	ID       string
	CtxLen   int64
}

// Catalog 聚合各 provider 的模型清单。零值不可用，经 NewCatalog 构造；
// 构造时从 store 恢复上次快照，重启后无需等首轮同步即可服务。
type Catalog struct {
	providers []provider.Provider
	// static 是注册表声明的静态清单（provider → models），
	// ErrNotSupported 与冷启动故障时的回落源。
	static map[string][]provider.ModelInfo
	// freeMeta 是 provider 级免费层说明（注册表 free_tier），随行落库。
	freeMeta map[string]string
	st       *store.Store // nil = 仅内存（测试/降级）
	log      *slog.Logger
	interval time.Duration
	// coldRetry 是冷启动重试间隔（测试注入缩短）；零值用 coldStartBackoff。
	coldRetry time.Duration

	mu     sync.RWMutex
	models map[string][]provider.ModelInfo
	sums   map[string]string
	// feedWindows/feedProbation/feedCapability 是 M6.5 签名 feed 并入的
	// 窗口/众测标注/能力分（provider → model → …）；窗口作 live 缺失时的
	// 回落源，capability 供 quality 策略排序（0=未评级）。
	feedWindows    map[string]map[string]int64
	feedProbation  map[string]map[string]bool
	feedCapability map[string]map[string]float64
}

// NewCatalog 装配目录并从 SQLite 恢复快照（有 store 时）。
func NewCatalog(
	providers []provider.Provider,
	static map[string][]provider.ModelInfo,
	freeMeta map[string]string,
	st *store.Store,
	log *slog.Logger,
) *Catalog {
	c := &Catalog{
		providers: providers,
		static:    static,
		freeMeta:  freeMeta,
		st:        st,
		log:       log,
		interval:  SyncInterval,
		models:    map[string][]provider.ModelInfo{},
		sums:      map[string]string{},
	}
	if st != nil {
		c.restore()
	}
	return c
}

// restore 从 models 表重建内存快照（重启恢复；FreeRide 教训：
// 持久化的状态不该等首轮同步才有）。
func (c *Catalog) restore() {
	rows, err := c.st.LoadModels()
	if err != nil {
		if c.log != nil {
			c.log.Warn("catalog restore failed; starting empty", "err", err)
		}
		return
	}
	byProvider := map[string][]provider.ModelInfo{}
	for _, r := range rows {
		byProvider[r.Provider] = append(byProvider[r.Provider],
			provider.ModelInfo{ID: r.ID, ContextWindow: r.CtxLen})
	}
	for name, list := range byProvider {
		c.models[name] = list
		c.sums[name] = catalogChecksum(list)
	}
	if c.log != nil && len(byProvider) > 0 {
		c.log.Info("catalog restored from store", "providers", len(byProvider), "models", len(rows))
	}
}

// Sync 拉一轮全量目录，返回本次发生变更的 provider 数。
// 逐家独立容错：一家失败不影响其余；失败保留旧快照（目录只增不抖），
// 冷启动无快照时回落静态清单。
func (c *Catalog) Sync(ctx context.Context) int {
	changed := 0
	for _, p := range c.providers {
		if ctx.Err() != nil {
			break
		}
		if c.syncOne(ctx, p) {
			changed++
		}
	}
	return changed
}

// syncOne 同步单个 provider：live 拉取 → ErrNotSupported 回落静态 →
// 故障保旧（冷启动回落静态）→ 校验和判变更 → 落库换新。
func (c *Catalog) syncOne(ctx context.Context, p provider.Provider) bool {
	name := p.Name()
	live, err := p.ListModels(ctx)
	var list []provider.ModelInfo
	switch {
	case err == nil:
		list = live
	case errors.Is(err, provider.ErrNotSupported):
		list = c.static[name] // 原生协议家（M6 前无实时面）
	default:
		c.mu.RLock()
		_, have := c.models[name]
		c.mu.RUnlock()
		if have {
			c.warnf("catalog sync failed; keeping last snapshot", name, err)
			return false
		}
		list = c.static[name]
		c.warnf("catalog sync failed on cold start; falling back to static list", name, err)
	}
	if len(list) == 0 {
		return false // 空清单不落库也不清空现有快照
	}

	sum := catalogChecksum(list)
	c.mu.RLock()
	unchanged := c.sums[name] == sum
	c.mu.RUnlock()
	if unchanged {
		return false // 校验和一致：跳过落库（1h 刷新不产生写放大）
	}
	if c.st != nil {
		rows := make([]store.ModelRow, 0, len(list))
		for _, m := range list {
			rows = append(rows, store.ModelRow{ID: m.ID, CtxLen: m.ContextWindow})
		}
		if err := c.st.ReplaceProviderModels(name, c.freeMeta[name], rows); err != nil {
			c.warnf("catalog persist failed; keeping in-memory snapshot only", name, err)
		}
	}
	c.mu.Lock()
	c.models[name] = list
	c.sums[name] = sum
	c.mu.Unlock()
	if c.log != nil {
		c.log.Info("catalog updated", "provider", name, "models", len(list))
	}
	return true
}

// Run 阻塞执行定时同步：启动立即一轮，随后按 interval 周期刷新，
// ctx 取消时返回。interval 包内可注入（测试缩短周期）。
// 首轮后仍有 provider 无任何快照（冷启动竞态，如容器编排里上游
// 晚于本进程就绪）时，先按短退避抢 coldStartRetries 轮补齐，
// 再进入常规周期——否则首个 interval 内 /v1/models 对该家为空。
func (c *Catalog) Run(ctx context.Context) {
	c.logSync(c.Sync(ctx))
	for i := 0; i < coldStartRetries && c.coldStartPending(); i++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.coldRetryInterval()):
		}
		c.logSync(c.Sync(ctx))
	}
	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.logSync(c.Sync(ctx))
		}
	}
}

// ServesModel 实现模型成员过滤（ModelMembership，docs/00 §4.5 遗留项）：
// live/静态快照含该模型（精确 id 或「厂商/模型」后缀——OpenRouter 风格
// 限定 id）视为可服务；无快照（目录未同步）返回 true（不确定不过滤）。
// 已知局限：registry model_aliases 重写不在此判定（当前零声明）。
func (c *Catalog) ServesModel(providerName, model string) bool {
	c.mu.RLock()
	list, ok := c.models[providerName]
	c.mu.RUnlock()
	if !ok {
		return true // 无快照：不确定，不过滤
	}
	for _, m := range list {
		if m.ID == model || strings.HasSuffix(m.ID, "/"+model) {
			return true
		}
	}
	return false
}

// coldStartPending 报告是否仍有 provider 连静态回落快照都没有
// （冷启动同步失败且注册表静态清单为空）。
func (c *Catalog) coldStartPending() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, p := range c.providers {
		if _, ok := c.models[p.Name()]; !ok {
			return true
		}
	}
	return false
}

func (c *Catalog) coldRetryInterval() time.Duration {
	if c.coldRetry > 0 {
		return c.coldRetry
	}
	return coldStartBackoff
}

func (c *Catalog) logSync(changed int) {
	if c.log != nil && changed > 0 {
		c.log.Info("catalog sync refreshed", "providers_changed", changed)
	}
}

// ContextWindow 返回 (provider, model) 的上下文窗口；优先 live 清单
// 的非零值，live 未收录或窗口为零时回落 M6.5 签名 feed（社区维护的
// 窗口数据——provider /models 几乎不报 context_length）。都无则
// ok=false（调用方不过滤）。实现 WindowResolver（M4.5 跨层）。
func (c *Catalog) ContextWindow(providerName, model string) (int64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, m := range c.models[providerName] {
		if m.ID == model {
			if m.ContextWindow > 0 {
				return m.ContextWindow, true
			}
			break // live 收录但无窗口：回落 feed
		}
	}
	if w, ok := c.feedWindows[providerName][model]; ok && w > 0 {
		return w, true
	}
	return 0, false
}

// Snapshot 返回按 (provider, id) 排序的平铺目录视图。
func (c *Catalog) Snapshot() []CatalogEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]CatalogEntry, 0, 64)
	for name, list := range c.models {
		for _, m := range list {
			out = append(out, CatalogEntry{Provider: name, ID: m.ID, CtxLen: m.ContextWindow})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Checksum 返回该 provider 当前清单的校验和（观测/测试用）；
// 未知 provider 返回空串。
func (c *Catalog) Checksum(name string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sums[name]
}

// catalogChecksum 是清单的内容指纹（校验和）：对排序后的
// "id\x00ctx_len" 行做 SHA-256。顺序无关，清单相同即同和。
func catalogChecksum(list []provider.ModelInfo) string {
	sorted := make([]provider.ModelInfo, len(list))
	copy(sorted, list)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	h := sha256.New()
	for _, m := range sorted {
		_, _ = fmt.Fprintf(h, "%s\x00%d\n", m.ID, m.ContextWindow)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (c *Catalog) warnf(msg, providerName string, err error) {
	if c.log != nil {
		c.log.Warn(msg, "provider", providerName, "err", err)
	}
}
