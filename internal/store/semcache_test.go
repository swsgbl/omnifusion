package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSemanticCacheRoundtrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	defer s.Close()

	// 未写入：未命中
	if _, err := s.GetSemanticCache("h1"); !errors.Is(err, ErrCacheMiss) {
		t.Fatalf("空库 Get = %v; want ErrCacheMiss", err)
	}

	if err := s.PutSemanticCache("h1", []byte(`{"id":"resp1"}`), 1000); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.GetSemanticCache("h1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got.Payload) != `{"id":"resp1"}` || got.Timestamp != 1000 {
		t.Fatalf("Get = %q, ts=%d; want resp1, 1000", got.Payload, got.Timestamp)
	}

	// 同键重写：upsert 覆盖
	if err := s.PutSemanticCache("h1", []byte(`{"id":"resp2"}`), 2000); err != nil {
		t.Fatalf("Put 覆盖: %v", err)
	}
	got, err = s.GetSemanticCache("h1")
	if err != nil || string(got.Payload) != `{"id":"resp2"}` || got.Timestamp != 2000 {
		t.Fatalf("覆盖后 Get = %q, ts=%d, %v; want resp2, 2000", got.Payload, got.Timestamp, err)
	}
}

func TestSemanticCacheTrimKeepsNewest(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	defer s.Close()

	for i := 0; i < 5; i++ {
		hash := string(rune('a' + i))
		if err := s.PutSemanticCache(hash, []byte{byte(i)}, int64(i)*10); err != nil {
			t.Fatalf("Put %s: %v", hash, err)
		}
	}

	n, err := s.TrimSemanticCache(3)
	if err != nil {
		t.Fatalf("Trim: %v", err)
	}
	if n != 2 {
		t.Errorf("Trim 删除 %d 行, want 2", n)
	}
	// 最旧两行（ts 0/10）被淘汰，最新三行保留
	for _, gone := range []string{"a", "b"} {
		if _, err := s.GetSemanticCache(gone); !errors.Is(err, ErrCacheMiss) {
			t.Errorf("%s 应被淘汰, err=%v", gone, err)
		}
	}
	for _, keep := range []string{"c", "d", "e"} {
		if _, err := s.GetSemanticCache(keep); err != nil {
			t.Errorf("%s 应保留: %v", keep, err)
		}
	}

	// 容量未超：零删除
	if n, err := s.TrimSemanticCache(10); err != nil || n != 0 {
		t.Errorf("Trim(10) = %d, %v; want 0, nil", n, err)
	}
}

// TestCountSemanticCache 验证 Dashboard 计数：空表为 0，写入
// 与 upsert（同 hash 覆盖不新增）后取值正确。
func TestCountSemanticCache(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	defer s.Close()

	n, err := s.CountSemanticCache()
	if err != nil {
		t.Fatalf("Count 空表: %v", err)
	}
	if n != 0 {
		t.Errorf("空表 count = %d, want 0", n)
	}

	for i, h := range []string{"h1", "h2", "h3"} {
		if err := s.PutSemanticCache(h, []byte{byte(i)}, int64(i)); err != nil {
			t.Fatalf("Put %s: %v", h, err)
		}
	}
	if n, err = s.CountSemanticCache(); err != nil || n != 3 {
		t.Errorf("count = %d, %v; want 3, nil", n, err)
	}

	// 同 hash upsert：覆盖不新增
	if err := s.PutSemanticCache("h2", []byte{9}, 99); err != nil {
		t.Fatalf("Put h2 覆盖: %v", err)
	}
	if n, err = s.CountSemanticCache(); err != nil || n != 3 {
		t.Errorf("upsert 后 count = %d, %v; want 3, nil", n, err)
	}
}
