// catalog_restore_test.go 覆盖目录冷启动恢复与窗口数据面。
package routing

import (
	"context"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/store"
	"path/filepath"
	"testing"
)

func TestCatalogRestoreFromStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.db")
	up := newModelsUpstream(t, upstreamModel{id: "m-a", ctx: 7})

	func() {
		st, err := store.Open(path)
		if err != nil {
			t.Fatalf("store.Open: %v", err)
		}
		defer st.Close()
		c := NewCatalog(
			[]provider.Provider{newMockAdapter(t, "live", up.srv.URL)},
			nil, nil, st, nil,
		)
		if got := c.Sync(context.Background()); got != 1 {
			t.Fatalf("Sync changed = %d, want 1", got)
		}
	}()

	st2, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st2.Close()
	c2 := NewCatalog(nil, nil, nil, st2, nil) // 无 providers：纯恢复

	snap := c2.Snapshot()
	if len(snap) != 1 || snap[0].Provider != "live" || snap[0].ID != "m-a" || snap[0].CtxLen != 7 {
		t.Fatalf("restored snapshot = %+v, want live/m-a ctx 7", snap)
	}
	if c2.Checksum("live") == "" {
		t.Error("restored checksum missing (gate would re-persist on next sync)")
	}
}

func TestCatalogContextWindow(t *testing.T) {
	up := newModelsUpstream(t,
		upstreamModel{id: "m-a", ctx: 8192},
		upstreamModel{id: "m-b", ctx: 4096},
	)
	c := NewCatalog([]provider.Provider{newMockAdapter(t, "live", up.srv.URL)}, nil, nil, newCatalogStore(t), nil)
	if c.Sync(context.Background()) != 1 {
		t.Fatal("sync failed")
	}
	if w, ok := c.ContextWindow("live", "m-a"); !ok || w != 8192 {
		t.Fatalf("ContextWindow(live, m-a) = (%d, %v), want (8192, true)", w, ok)
	}
	if w, ok := c.ContextWindow("live", "m-b"); !ok || w != 4096 {
		t.Fatalf("ContextWindow(live, m-b) = (%d, %v), want (4096, true)", w, ok)
	}
	if _, ok := c.ContextWindow("live", "missing"); ok {
		t.Fatal("unknown model must report ok=false")
	}
	if _, ok := c.ContextWindow("other", "m-a"); ok {
		t.Fatal("unknown provider must report ok=false")
	}
}
