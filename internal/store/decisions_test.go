// decisions_test.go 锁死路由决策回放记录契约：行往返（bool/int 列
// 与 evidence JSON）、ts 倒序、默认上限。
package store

import "testing"

func TestRouteDecisionRoundTrip(t *testing.T) {
	st, _ := newTestStore(t)
	rows := []RouteDecisionRow{
		{TS: 100, Endpoint: "chat", RequestID: "r1", Model: "@quality",
			ChosenProvider: "zhipu", Success: true, Tries: 2, DurationMS: 850,
			Evidence: `{"reason_codes":["FAILOVER_CHOSEN"]}`},
		{TS: 200, Endpoint: "a2a", RequestID: "r2", Model: "m",
			ChosenProvider: "", Success: false, Tries: 3, DurationMS: 30,
			Evidence: `{"reason_codes":["ALL_CANDIDATES_EXHAUSTED"]}`},
	}
	for _, r := range rows {
		if err := st.InsertRouteDecision(r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	got, err := st.LoadRecentRouteDecisions(10)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	// ts 倒序：新者在前。
	if got[0].TS != 200 || got[0].Endpoint != "a2a" || got[0].Success {
		t.Fatalf("row[0] = %+v, want ts=200 a2a failed", got[0])
	}
	if got[1].Success != true || got[1].Tries != 2 || got[1].ChosenProvider != "zhipu" {
		t.Fatalf("row[1] = %+v", got[1])
	}
	if got[1].Evidence == "" || got[0].Evidence == "" {
		t.Fatal("evidence JSON missing")
	}
}

func TestRouteDecisionDefaultLimit(t *testing.T) {
	st, _ := newTestStore(t)
	for i := 0; i < 60; i++ {
		if err := st.InsertRouteDecision(RouteDecisionRow{TS: int64(i), Endpoint: "chat"}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	got, err := st.LoadRecentRouteDecisions(0) // <=0 → 默认 50
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 50 {
		t.Fatalf("default limit rows = %d, want 50", len(got))
	}
	if got[0].TS != 59 {
		t.Fatalf("newest first violated: ts=%d", got[0].TS)
	}
}
