package store

import (
	"path/filepath"
	"testing"
)

func TestOpenCreatesSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	defer s.Close()

	if err := s.SetMeta("schema_version_note", "v1"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	got, err := s.GetMeta("schema_version_note")
	if err != nil || got != "v1" {
		t.Fatalf("GetMeta = %q, %v; want v1", got, err)
	}
}

func TestMigrationIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("首次打开失败: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	// 重开：迁移应全部跳过，不报错
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("重开失败（迁移不幂等？）: %v", err)
	}
	defer s2.Close()

	var count int
	if err := s2.DB().QueryRow(
		`SELECT COUNT(*) FROM schema_migrations`,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(migrations) {
		t.Errorf("迁移记账 %d 条, want %d", count, len(migrations))
	}
}

func TestGetMetaMissingReturnsEmpty(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got, err := s.GetMeta("no_such_key")
	if err != nil || got != "" {
		t.Errorf("GetMeta(不存在) = %q, %v; want 空串", got, err)
	}
}
