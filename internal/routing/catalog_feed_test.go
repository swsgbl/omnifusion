package routing

import (
	"context"
	"testing"

	"github.com/swsgbl/omnifusion/internal/catalogfeed"
	"github.com/swsgbl/omnifusion/internal/provider"
)

// feedFor 构造一个已通过验签阶段的 feed 视图（ApplyFeed 只消费结构）。
func feedFor(version int64, models map[string][]catalogfeed.ModelEntry) *catalogfeed.Feed {
	providers := map[string]catalogfeed.ProviderFeed{}
	for name, ms := range models {
		providers[name] = catalogfeed.ProviderFeed{FreeTier: "test", Models: ms}
	}
	return &catalogfeed.Feed{Version: version, GeneratedAt: 1_700_000_000, Providers: providers}
}

// TestApplyFeedFillsWindows 是 M6.5 窗口补齐验收：live 非零窗口优先，
// live 收录但 ctx=0 回落 feed，live 未收录同样由 feed 补上——三者
// 共同构成窗口过滤（M4.5）的数据来源。
func TestApplyFeedFillsWindows(t *testing.T) {
	up := newModelsUpstream(t,
		upstreamModel{id: "m-live", ctx: 8192},
		upstreamModel{id: "m-noc", ctx: 0}, // live 清单不含窗口
	)
	st := newCatalogStore(t)
	c := NewCatalog([]provider.Provider{newMockAdapter(t, "live", up.srv.URL)}, nil, nil, st, nil)
	c.Sync(context.Background())

	if got := c.ApplyFeed(nil); got != 0 {
		t.Fatalf("ApplyFeed(nil) = %d, want 0", got)
	}
	n := c.ApplyFeed(feedFor(1, map[string][]catalogfeed.ModelEntry{
		"live": {
			{ID: "m-live", CtxLen: 111}, // 应被 live 8192 胜出
			{ID: "m-noc", CtxLen: 2048}, // live ctx=0 → feed 补
			{ID: "m-z", CtxLen: 4096},   // live 未收录 → feed 补
		},
	}))
	if n != 3 {
		t.Fatalf("ApplyFeed entries = %d, want 3", n)
	}

	cases := []struct {
		model string
		want  int64
	}{
		{"m-live", 8192}, // live 非零优先
		{"m-noc", 2048},  // live 零窗口回落 feed
		{"m-z", 4096},    // live 未收录由 feed 补
	}
	for _, tc := range cases {
		if got, ok := c.ContextWindow("live", tc.model); !ok || got != tc.want {
			t.Errorf("ContextWindow(live, %s) = (%d, %v), want (%d, true)", tc.model, got, ok, tc.want)
		}
	}
	if _, ok := c.ContextWindow("live", "m-none"); ok {
		t.Error("ContextWindow(m-none) = true, want false")
	}

	// 同版本重复应用幂等（整组覆盖写）。
	c.ApplyFeed(feedFor(1, map[string][]catalogfeed.ModelEntry{
		"live": {
			{ID: "m-noc", CtxLen: 2048},
			{ID: "m-z", CtxLen: 4096},
			{ID: "m-live", CtxLen: 111},
		},
	}))
	if got, _ := c.ContextWindow("live", "m-noc"); got != 2048 {
		t.Fatalf("reapply changed m-noc window to %d", got)
	}
}

// TestFeedProbation 验证众测状态只做观测标注。
func TestFeedProbation(t *testing.T) {
	st := newCatalogStore(t)
	c := NewCatalog(nil, nil, nil, st, nil)
	c.ApplyFeed(feedFor(2, map[string][]catalogfeed.ModelEntry{
		"demo": {
			{ID: "m-prob", CtxLen: 4096, Status: catalogfeed.StatusProbation},
			{ID: "m-ok", CtxLen: 8192, Status: catalogfeed.StatusStable},
		},
	}))
	if !c.FeedProbation("demo", "m-prob") {
		t.Error("probation entry not flagged")
	}
	if c.FeedProbation("demo", "m-ok") {
		t.Error("stable entry flagged probation")
	}
	if c.FeedProbation("demo", "m-none") || c.FeedProbation("other", "m-prob") {
		t.Error("unknown entries flagged probation")
	}
}

// TestFeedSnapshot 验证平铺视图排序与字段。
func TestFeedSnapshot(t *testing.T) {
	st := newCatalogStore(t)
	c := NewCatalog(nil, nil, nil, st, nil)
	c.ApplyFeed(feedFor(3, map[string][]catalogfeed.ModelEntry{
		"b-prov": {{ID: "m-2", CtxLen: 1}, {ID: "m-1", CtxLen: 2, Status: catalogfeed.StatusProbation}},
		"a-prov": {{ID: "m-x", CtxLen: 3}},
	}))
	snap := c.FeedSnapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot len = %d, want 3", len(snap))
	}
	wantOrder := []string{"a-prov/m-x", "b-prov/m-1", "b-prov/m-2"}
	for i, e := range snap {
		got := e.Provider + "/" + e.ID
		if got != wantOrder[i] {
			t.Errorf("snap[%d] = %s, want %s", i, got, wantOrder[i])
		}
	}
	if !snap[1].Probation || snap[0].Probation || snap[2].Probation {
		t.Errorf("probation flags = [%v %v %v], want [false true false]",
			snap[0].Probation, snap[1].Probation, snap[2].Probation)
	}
}
