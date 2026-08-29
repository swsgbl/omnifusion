package intelligence

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/store"
)

func keyReq() *schema.UnifiedRequest {
	temp := 0.7
	return &schema.UnifiedRequest{
		Model: "m1",
		Messages: []schema.Message{
			{Role: "user", Content: schema.NewTextContent("ping")},
		},
		Temperature: &temp,
	}
}

func TestCacheKeyDeterminism(t *testing.T) {
	a, b := keyReq(), keyReq()
	if CacheKey(a) != CacheKey(b) {
		t.Fatal("同请求两次计算键不同")
	}

	// Stream 不参与键
	c := keyReq()
	c.Stream = true
	if CacheKey(c) != CacheKey(a) {
		t.Error("Stream 标志不应影响缓存键")
	}

	// 任一参与字段变化 → 键变化
	variants := map[string]*schema.UnifiedRequest{
		"model": func() *schema.UnifiedRequest { r := keyReq(); r.Model = "m2"; return r }(),
		"messages": func() *schema.UnifiedRequest {
			r := keyReq()
			r.Messages = append(r.Messages, schema.Message{Role: "user", Content: schema.NewTextContent("more")})
			return r
		}(),
		"temperature": func() *schema.UnifiedRequest {
			r := keyReq()
			temp := 0.9
			r.Temperature = &temp
			return r
		}(),
	}
	base := CacheKey(a)
	for name, r := range variants {
		if CacheKey(r) == base {
			t.Errorf("改变 %s 后键仍相同", name)
		}
	}
}

func TestSemCacheRoundtrip(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	c := NewSemCache(st, time.Hour, 16)
	req := keyReq()
	resp := schema.NewResponse("resp-1", "m1", 100)

	if got, ok := c.Lookup(context.Background(), req); ok {
		t.Fatalf("空缓存 Lookup 命中: %+v", got)
	}
	c.WriteBack(context.Background(), req, resp)

	got, ok := c.Lookup(context.Background(), req)
	if !ok {
		t.Fatal("回写后 Lookup 未命中")
	}
	if got.ID != "resp-1" || got.Object != "chat.completion" || len(got.Choices) != 0 {
		t.Errorf("命中内容 = %+v", got)
	}
	// 修改响应不影响缓存内副本（值拷贝语义）
	got.ID = "mutated"
	again, _ := c.Lookup(context.Background(), req)
	if again.ID != "resp-1" {
		t.Errorf("缓存被调用方修改污染: %q", again.ID)
	}
}

func TestSemCacheTTLExpiry(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	c := NewSemCache(st, time.Hour, 16)

	t0 := time.Unix(1700000000, 0)
	c.now = func() time.Time { return t0 }
	req := keyReq()
	c.WriteBack(context.Background(), req, schema.NewResponse("r", "m1", 1))

	c.now = func() time.Time { return t0.Add(59 * time.Minute) }
	if _, ok := c.Lookup(context.Background(), req); !ok {
		t.Error("TTL 内应命中")
	}
	c.now = func() time.Time { return t0.Add(61 * time.Minute) }
	if _, ok := c.Lookup(context.Background(), req); ok {
		t.Error("TTL 过期后不应命中")
	}
}

func TestSemCacheStreamBypassAndNilSafe(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	c := NewSemCache(st, time.Hour, 16)

	req := keyReq()
	req.Stream = true
	c.WriteBack(context.Background(), req, schema.NewResponse("r", "m1", 1))
	if _, ok := c.Lookup(context.Background(), req); ok {
		t.Error("流式请求不应查/写缓存")
	}

	var nilCache *SemCache
	if _, ok := nilCache.Lookup(context.Background(), keyReq()); ok {
		t.Error("nil 缓存 Lookup 应安全返回未命中")
	}
	nilCache.WriteBack(context.Background(), keyReq(), nil) // 不得 panic
}

func TestSemCacheTrimEviction(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	c := NewSemCache(st, time.Hour, 8)

	// 65 次回写（ts 逐次 +1s 保递归时序）：第 64 次触发 Trim(8)
	t0 := time.Unix(1700000000, 0)
	for i := 0; i < 65; i++ {
		c.now = func() time.Time { return t0.Add(time.Duration(i) * time.Second) }
		req := keyReq()
		req.Messages = []schema.Message{
			{Role: "user", Content: schema.NewTextContent("q" + string(rune('a'+i%26)) + "-" + itoa(i))},
		}
		c.WriteBack(context.Background(), req, schema.NewResponse("r", "m1", 1))
	}
	c.now = func() time.Time { return t0.Add(65 * time.Second) }
	first := keyReq()
	first.Messages = []schema.Message{{Role: "user", Content: schema.NewTextContent("qa-0")}}
	if _, ok := c.Lookup(context.Background(), first); ok {
		t.Error("最旧条目应被容量淘汰")
	}
	last := keyReq()
	last.Messages = []schema.Message{{Role: "user", Content: schema.NewTextContent("qm-64")}}
	if _, ok := c.Lookup(context.Background(), last); !ok {
		t.Error("最新条目应保留")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
