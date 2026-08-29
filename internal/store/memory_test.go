package store

import (
	"path/filepath"
	"testing"
)

// TestSessionMemoryRoundtrip 覆盖 v8 迁移建表、插入、FTS5 检索（EN 词
// 与 CJK bigram——unicode61 下 bigram 短语等价相邻单字匹配，中文检索
// 由此生效）、limit、计数与按写入序淘汰。tokens 串照
// intelligence.memoryTokens 的口径手写（store 不得反向依赖
// intelligence，测试亦然）。
func TestSessionMemoryRoundtrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	defer s.Close()

	// 空库检索：空结果而非错误
	if rows, err := s.SearchSessionMemory(`"gateway"`, 4); err != nil || len(rows) != 0 {
		t.Fatalf("空库检索 = %v, %v; want 空", rows, err)
	}

	// EN：tokens 即小写词空格串
	if err := s.InsertSessionMemory("s1", "user",
		"deploy the gateway now", "deploy the gateway now", 100); err != nil {
		t.Fatalf("插入 EN: %v", err)
	}
	// CJK：tokens 为 bigram 空格串（「网关部署完成」→ 网关/关部/部署/署完/完成）
	if err := s.InsertSessionMemory("s2", "user",
		"网关部署完成", "网关 关部 部署 署完 完成", 200); err != nil {
		t.Fatalf("插入 CJK: %v", err)
	}

	rows, err := s.SearchSessionMemory(`"gateway"`, 4)
	if err != nil || len(rows) != 1 {
		t.Fatalf("EN 词检索 = %v, %v; want 1 行", rows, err)
	}
	if rows[0].SessionID != "s1" || rows[0].Role != "user" ||
		rows[0].Content != "deploy the gateway now" || rows[0].CreatedUnix != 100 {
		t.Fatalf("EN 行字段 = %+v", rows[0])
	}

	rows, err = s.SearchSessionMemory(`"部署"`, 4)
	if err != nil || len(rows) != 1 || rows[0].SessionID != "s2" ||
		rows[0].Content != "网关部署完成" {
		t.Fatalf("CJK bigram 检索 = %v, %v; want s2 原文", rows, err)
	}

	// 无关词：空结果
	if rows, err := s.SearchSessionMemory(`"zzz"`, 4); err != nil || len(rows) != 0 {
		t.Fatalf("无关词检索 = %v, %v; want 空", rows, err)
	}

	// limit 生效：双词 OR 命中 2 行但只要 1 行
	rows, err = s.SearchSessionMemory(`"gateway" OR "部署"`, 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("limit 检索 = %v, %v; want 1 行", rows, err)
	}

	if n, err := s.CountSessionMemory(); err != nil || n != 2 {
		t.Fatalf("计数 = %d, %v; want 2", n, err)
	}

	// 淘汰到 1 行：保留写入序最新（s2），删 s1
	if n, err := s.TrimSessionMemory(1); err != nil || n != 1 {
		t.Fatalf("淘汰 = %d, %v; want 删 1", n, err)
	}
	if n, err := s.CountSessionMemory(); err != nil || n != 1 {
		t.Fatalf("淘汰后计数 = %d, %v; want 1", n, err)
	}
	if rows, err := s.SearchSessionMemory(`"gateway"`, 4); err != nil || len(rows) != 0 {
		t.Fatalf("被淘汰行仍可检索 = %v, %v", rows, err)
	}
	if rows, err := s.SearchSessionMemory(`"部署"`, 4); err != nil || len(rows) != 1 {
		t.Fatalf("保留行不可检索 = %v, %v", rows, err)
	}
}
