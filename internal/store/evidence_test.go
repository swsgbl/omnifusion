package store

import (
	"path/filepath"
	"testing"
	"time"
)

// TestQueryModelEvidence 是 众测证据面验收：按 (provider, model)
// 聚合窗口内的请求结果，空 model 与窗口外/他 provider 不计。
func TestQueryModelEvidence(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "evidence.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	now := time.Now().Unix()
	rows := []RequestLog{
		{TS: now - 10, Provider: "demo", Model: "m-a", Status: 200},
		{TS: now - 20, Provider: "demo", Model: "m-a", Status: 200},
		{TS: now - 30, Provider: "demo", Model: "m-a", Status: 429},
		{TS: now - 40, Provider: "demo", Model: "m-b", Status: 500},
		{TS: now - 50, Provider: "demo", Model: "", Status: 200},         // 未分发，不计
		{TS: now - 60, Provider: "other", Model: "m-a", Status: 200},     // 他 provider
		{TS: now - 999_999, Provider: "demo", Model: "m-a", Status: 200}, // 窗口外
	}
	for i, r := range rows {
		if err := s.InsertRequestLog(r); err != nil {
			t.Fatalf("InsertRequestLog[%d]: %v", i, err)
		}
	}

	ev, err := s.QueryModelEvidence("demo", now-3600)
	if err != nil {
		t.Fatalf("QueryModelEvidence: %v", err)
	}
	if len(ev) != 2 {
		t.Fatalf("evidence rows = %d, want 2 (m-a, m-b): %+v", len(ev), ev)
	}
	// m-a 调用数更多，降序在前。
	if ev[0].Model != "m-a" || ev[1].Model != "m-b" {
		t.Fatalf("order = [%s %s], want [m-a m-b]", ev[0].Model, ev[1].Model)
	}
	if ev[0].Calls != 3 || ev[0].OK != 2 || ev[0].Errors != 1 {
		t.Errorf("m-a = %+v, want Calls 3 OK 2 Errors 1", ev[0])
	}
	if ev[1].Calls != 1 || ev[1].OK != 0 || ev[1].Errors != 1 {
		t.Errorf("m-b = %+v, want Calls 1 OK 0 Errors 1", ev[1])
	}

	// 无数据的 provider 返回空切片（非 nil 也无妨，长度必须为 0）。
	none, err := s.QueryModelEvidence("ghost", now-3600)
	if err != nil {
		t.Fatalf("QueryModelEvidence(ghost): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("ghost evidence = %+v, want empty", none)
	}
}
