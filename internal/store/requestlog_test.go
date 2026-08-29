package store

import (
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestRequestLogCRUD：插入→倒序查询→过滤→计数→裁剪全链路。
func TestRequestLogCRUD(t *testing.T) {
	s := openTestStore(t)
	rows := []RequestLog{
		{TS: 100, Endpoint: "chat", Model: "m1", Provider: "groq", Status: 200,
			TokensIn: 10, TokensOut: 5, LatencyMS: 120.5, TTFTMS: -1, Combo: "fast"},
		{TS: 200, Endpoint: "messages", Model: "m2", Provider: "cache", Status: 200,
			LatencyMS: 2.1, CacheHit: true},
		{TS: 300, Endpoint: "gemini", Model: "m3", Provider: "", Status: 400,
			LatencyMS: 0.4, ErrKind: "guardrails"},
		{TS: 400, Endpoint: "chat", Model: "m1", Provider: "groq", Status: 502,
			LatencyMS: 3000, ErrKind: "rate_limit"},
	}
	for i, r := range rows {
		if err := s.InsertRequestLog(r); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	got, err := s.QueryRequestLogs(RequestLogFilter{})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 4 || got[0].TS != 400 || got[3].TS != 100 {
		t.Fatalf("query order = %+v, want ts desc 400..100", got)
	}
	if got[2].CacheHit != true || got[1].ErrKind != "guardrails" {
		t.Errorf("roundtrip fields wrong: %+v %+v", got[1], got[2])
	}

	// 过滤：provider + since + limit。
	onlyGroq, err := s.QueryRequestLogs(RequestLogFilter{Provider: "groq"})
	if err != nil || len(onlyGroq) != 2 {
		t.Errorf("provider filter = %d rows, err %v; want 2", len(onlyGroq), err)
	}
	recent, err := s.QueryRequestLogs(RequestLogFilter{Since: 250})
	if err != nil || len(recent) != 2 {
		t.Errorf("since filter = %d rows, err %v; want 2", len(recent), err)
	}
	lim, err := s.QueryRequestLogs(RequestLogFilter{Limit: 1})
	if err != nil || len(lim) != 1 || lim[0].TS != 400 {
		t.Errorf("limit filter = %+v, err %v", lim, err)
	}
	if n, _ := s.CountRequestLogs(); n != 4 {
		t.Errorf("count = %d, want 4", n)
	}

	// 裁剪：保留最新 2 行 → 裁掉 2 行。
	if n, err := s.PruneRequestLogs(2); err != nil || n != 2 {
		t.Fatalf("prune = %d, err %v; want 2", n, err)
	}
	if n, _ := s.CountRequestLogs(); n != 2 {
		t.Errorf("count after prune = %d, want 2", n)
	}
	after, _ := s.QueryRequestLogs(RequestLogFilter{})
	if after[0].TS != 400 || after[1].TS != 300 {
		t.Errorf("prune kept wrong rows: %+v", after)
	}
	// 裁剪超额 keep（行数不足）应为 0 且不报错。
	if n, err := s.PruneRequestLogs(1000); err != nil || n != 0 {
		t.Errorf("over-prune = %d, err %v; want 0", n, err)
	}
}
