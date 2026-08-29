package routing

import (
	"context"
	"encoding/json"
	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// upstreamModel 是一条目录记录；modelsUpstream 是可热替的
// /v1/models 上游（ids 可变、fail 翻 500），驱动 catalog 同步路径。
type upstreamModel struct {
	id  string
	ctx int64
}

type modelsUpstream struct {
	mu   sync.Mutex
	ids  []upstreamModel
	fail bool
	srv  *httptest.Server
}

func newModelsUpstream(t *testing.T, ids ...upstreamModel) *modelsUpstream {
	t.Helper()
	u := &modelsUpstream{ids: ids}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		u.mu.Lock()
		defer u.mu.Unlock()
		if u.fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		type row struct {
			ID            string `json:"id"`
			ContextLength int64  `json:"context_length"`
		}
		rows := make([]row, 0, len(u.ids))
		for _, m := range u.ids {
			rows = append(rows, row{ID: m.id, ContextLength: m.ctx})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": rows})
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *modelsUpstream) set(ids ...upstreamModel) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.ids = ids
}

func (u *modelsUpstream) setFail(f bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.fail = f
}

// newCatalogStore 开一个 TempDir 里的 SQLite（routing 包测试自用）。
func newCatalogStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// stubProvider 是全接口 stub：catalog 只消费 Name/ListModels，
// 其余方法返回 ErrNotSupported 占位（避免嵌入 nil 接口 panic）。
type stubProvider struct {
	name   string
	models []provider.ModelInfo
	err    error
}

func (p *stubProvider) Name() string                      { return p.name }
func (p *stubProvider) Capabilities() provider.Capability { return provider.Capability{} }
func (p *stubProvider) Translate(context.Context, *schema.UnifiedRequest) (*provider.ProviderCall, error) {
	return nil, provider.ErrNotSupported
}
func (p *stubProvider) Parse(context.Context, *provider.ProviderCall, *http.Response) (*schema.Response, error) {
	return nil, provider.ErrNotSupported
}
func (p *stubProvider) ListModels(context.Context) ([]provider.ModelInfo, error) {
	return p.models, p.err
}
func (p *stubProvider) HTTPClient() *http.Client { return http.DefaultClient }

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}
