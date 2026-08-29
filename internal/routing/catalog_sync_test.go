// catalog_sync_test.go 覆盖目录同步：校验和门控、静态回退、故障保旧与周期刷新。
package routing

import (
	"context"
	"github.com/swsgbl/omnifusion/internal/provider"
	"testing"
	"time"
)

func TestCatalogSyncChecksumGate(t *testing.T) {
	up := newModelsUpstream(t,
		upstreamModel{id: "m-a", ctx: 8192},
		upstreamModel{id: "m-b", ctx: 4096},
	)
	st := newCatalogStore(t)
	c := NewCatalog([]provider.Provider{newMockAdapter(t, "live", up.srv.URL)}, nil, nil, st, nil)

	if got := c.Sync(context.Background()); got != 1 {
		t.Fatalf("first Sync changed = %d, want 1", got)
	}
	sum := c.Checksum("live")
	if sum == "" {
		t.Fatal("checksum empty after first sync")
	}

	// 同一清单换序：校验和（顺序无关）一致 → 跳过，不算变更。
	up.set(
		upstreamModel{id: "m-b", ctx: 4096},
		upstreamModel{id: "m-a", ctx: 8192},
	)
	if got := c.Sync(context.Background()); got != 0 {
		t.Fatalf("reordered Sync changed = %d, want 0", got)
	}
	if c.Checksum("live") != sum {
		t.Error("checksum changed after reorder")
	}

	// 清单真变 → 变更 + 快照与库同步换新。
	up.set(upstreamModel{id: "m-a", ctx: 8192}, upstreamModel{id: "m-c", ctx: 1024})
	if got := c.Sync(context.Background()); got != 1 {
		t.Fatalf("changed list Sync changed = %d, want 1", got)
	}
	snap := c.Snapshot()
	if len(snap) != 2 || snap[0].ID != "m-a" || snap[1].ID != "m-c" {
		t.Fatalf("snapshot = %+v, want [m-a m-c]", snap)
	}
	rows, err := st.LoadModels()
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != "m-a" || rows[1].ID != "m-c" {
		t.Fatalf("store rows = %+v, want [m-a m-c]", rows)
	}
}

func TestCatalogSyncStaticFallback(t *testing.T) {
	st := newCatalogStore(t)
	static := map[string][]provider.ModelInfo{
		"native": {{ID: "native-1", ContextWindow: 2000}, {ID: "native-2"}},
	}
	c := NewCatalog(
		[]provider.Provider{&stubProvider{name: "native", err: provider.ErrNotSupported}},
		static, map[string]string{"native": "free:100k tok/day"}, st, nil,
	)

	if got := c.Sync(context.Background()); got != 1 {
		t.Fatalf("Sync changed = %d, want 1", got)
	}
	rows, err := st.LoadModels()
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want 2 static entries", rows)
	}
	for _, r := range rows {
		if r.Provider != "native" || r.FreeMeta != "free:100k tok/day" {
			t.Errorf("row = %+v, want provider native with free_meta", r)
		}
	}
	snap := c.Snapshot()
	if len(snap) != 2 || snap[0].ID != "native-1" || snap[1].CtxLen != 0 {
		t.Fatalf("snapshot = %+v, want static list served", snap)
	}
}

func TestCatalogSyncFailureKeepsSnapshot(t *testing.T) {
	up := newModelsUpstream(t, upstreamModel{id: "m-a", ctx: 100})
	st := newCatalogStore(t)
	c := NewCatalog(
		[]provider.Provider{newMockAdapter(t, "live", up.srv.URL)},
		nil, nil, st, nil,
	)
	c.Sync(context.Background())
	before := c.Checksum("live")

	up.setFail(true)
	if got := c.Sync(context.Background()); got != 0 {
		t.Fatalf("failed Sync changed = %d, want 0", got)
	}
	if c.Checksum("live") != before {
		t.Error("checksum changed after upstream failure")
	}
	if snap := c.Snapshot(); len(snap) != 1 || snap[0].ID != "m-a" {
		t.Errorf("snapshot = %+v, want old m-a preserved", snap)
	}

	// 故障恢复且清单未变：仍命中校验和门控，不产生写放大。
	up.setFail(false)
	if got := c.Sync(context.Background()); got != 0 {
		t.Fatalf("recovered Sync changed = %d, want 0", got)
	}
}

func TestCatalogSyncColdStartFailureFallsBackToStatic(t *testing.T) {
	up := newModelsUpstream(t, upstreamModel{id: "m-a", ctx: 100})
	up.setFail(true)
	static := map[string][]provider.ModelInfo{"live": {{ID: "static-1"}}}
	c := NewCatalog(
		[]provider.Provider{newMockAdapter(t, "live", up.srv.URL)},
		static, nil, nil, nil, // st=nil：顺带覆盖纯内存模式
	)

	if got := c.Sync(context.Background()); got != 1 {
		t.Fatalf("Sync changed = %d, want 1", got)
	}
	snap := c.Snapshot()
	if len(snap) != 1 || snap[0].Provider != "live" || snap[0].ID != "static-1" {
		t.Fatalf("snapshot = %+v, want static fallback on cold start", snap)
	}
}

func TestCatalogRunRefreshesPeriodically(t *testing.T) {
	up := newModelsUpstream(t, upstreamModel{id: "m-a", ctx: 1})
	c := NewCatalog(
		[]provider.Provider{newMockAdapter(t, "live", up.srv.URL)},
		nil, nil, nil, nil,
	)
	c.interval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	// 启动立即一轮：m-a 可见。
	waitFor(t, 2*time.Second, func() bool {
		s := c.Snapshot()
		return len(s) == 1 && s[0].ID == "m-a"
	})
	// 翻清单后，下一个 tick 内刷新为 m-b（1h 刷新验收的注入版）。
	up.set(upstreamModel{id: "m-b", ctx: 2})
	waitFor(t, 2*time.Second, func() bool {
		s := c.Snapshot()
		return len(s) == 1 && s[0].ID == "m-b"
	})
	cancel()
}

// 冷启动竞态：上游在网关进程启动瞬间不可达（容器编排里上游晚于
// 网关就绪的形态）。Run 须按短退避补齐目录，而非空等一个 interval。
func TestCatalogRunRetriesColdStartFailure(t *testing.T) {
	up := newModelsUpstream(t, upstreamModel{id: "m-a", ctx: 8})
	up.setFail(true) // 首轮同步必失败；无静态回落 → 空目录
	c := NewCatalog(
		[]provider.Provider{newMockAdapter(t, "live", up.srv.URL)},
		nil, nil, nil, nil,
	)
	c.coldRetry = 25 * time.Millisecond
	c.interval = time.Hour // 常规 tick 不参与补齐

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	// 首轮同步必然已失败落定；此刻恢复上游，逼迫补齐只能来自短退避重试。
	time.Sleep(50 * time.Millisecond)
	up.setFail(false)
	waitFor(t, 2*time.Second, func() bool {
		s := c.Snapshot()
		return len(s) == 1 && s[0].ID == "m-a"
	})
	cancel()
}
