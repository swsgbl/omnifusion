package intelligence

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/store"
)

// Cache 2.0 代际失效测试：目录代际递增后，同请求的缓存键随之改变——
// 旧条目自然过期（resolved-model 维度的失效，蓝图 CacheKeySpec）。

func genReq() *schema.UnifiedRequest {
	t0 := 0.0
	sd := int64(7)
	return &schema.UnifiedRequest{
		Model: "m1",
		Messages: []schema.Message{
			{Role: "user", Content: schema.NewTextContent("ping")},
		},
		Temperature: &t0,
		Seed:        &sd,
	}
}

func TestGenerationInvalidatesCache(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "gen.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	gen := uint64(0)
	c := NewSemCache(st, time.Hour, 16)
	c.SetGenerationFunc(func() uint64 { return gen })

	req := genReq()
	resp := schema.NewResponse("resp-gen0", "m1", 100)
	c.WriteBack(context.Background(), req, resp)
	if _, ok := c.Lookup(context.Background(), req); !ok {
		t.Fatal("gen 0: write-back then lookup must hit")
	}

	// 目录代际递增：同请求键变化 → 旧条目不再命中
	gen = 1
	if _, ok := c.Lookup(context.Background(), req); ok {
		t.Fatal("gen 1: stale entry from gen 0 must not be served")
	}
	// 新代际写入后命中
	resp2 := schema.NewResponse("resp-gen1", "m1", 100)
	c.WriteBack(context.Background(), req, resp2)
	got, ok := c.Lookup(context.Background(), req)
	if !ok || got.ID != "resp-gen1" {
		t.Fatalf("gen 1 lookup = %+v ok=%v", got, ok)
	}
}

func TestGenerationZeroKeepsCompat(t *testing.T) {
	// 未装配 genFn（nil）→ 代际恒 0 → 与 CacheKey(req) 兼容（旧调用方）。
	st, err := store.Open(filepath.Join(t.TempDir(), "compat.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	c := NewSemCache(st, time.Hour, 16)
	req := genReq()
	resp := schema.NewResponse("r", "m1", 100)
	c.WriteBack(context.Background(), req, resp)
	if _, ok := c.Lookup(context.Background(), req); !ok {
		t.Fatal("nil genFn must behave like generation 0")
	}
	if CacheKey(req) != c.keyFor(req) {
		t.Error("nil genFn key must equal CacheKey(req)")
	}
}
