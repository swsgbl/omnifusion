// costledger_test.go 覆盖成本账本：定价折算、日聚合、meta 持久化
// 重启恢复、usage API 成本字段。
package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/store"
)

func testPriceFn(providerName, model string) (provider.Price, bool) {
	if providerName == "paid" {
		// $3/M in, $15/M out —— 1M+1M tokens = $18。
		return provider.Price{In: 3, Out: 15}, true
	}
	return provider.Price{}, false // free/unpriced = 0
}

func newTestLedger(t *testing.T, st *store.Store) *CostLedger {
	t.Helper()
	return NewCostLedger(st, slog.New(slog.NewTextHandler(io.Discard, nil)), testPriceFn)
}

func TestCostLedgerEstimateAndRecord(t *testing.T) {
	l := newTestLedger(t, nil)
	if usd := l.Estimate("paid", "m", 1_000_000, 1_000_000); usd != 18.0 {
		t.Fatalf("estimate = %v, want 18", usd)
	}
	if usd := l.Estimate("free", "m", 999_999, 999_999); usd != 0 {
		t.Fatalf("unpriced = %v, want 0", usd)
	}
	l.Record("paid", "m", 100_000, 100_000) // $1.8
	l.Record("paid", "m2", 100_000, 0)      // $0.3
	l.Record("free", "m", 500_000, 500_000) // 0
	total, byP, reqs := l.TodaySnapshot()
	if total < 2.09 || total > 2.11 {
		t.Fatalf("total = %v, want ~2.1", total)
	}
	if byP["paid"] != total || reqs != 3 {
		t.Fatalf("byProvider/requests = %v/%d", byP, reqs)
	}
}

func TestCostLedgerPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	l1 := newTestLedger(t, st)
	l1.Record("paid", "m", 200_000, 200_000) // $3.6
	if total := l1.TodayTotal(); total < 3.59 || total > 3.61 {
		t.Fatalf("before restart = %v", total)
	}
	st.Close()

	st2, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	l2 := newTestLedger(t, st2)
	if total := l2.TodayTotal(); total < 3.59 || total > 3.61 {
		t.Fatalf("after restart = %v, want persisted ~3.6", total)
	}
}

// TestUsageAPIIncludesCost usage 端点带成本汇总与行级成本。
func TestUsageAPIIncludesCost(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), st))
	s.SetCostLedger(newTestLedger(t, st))
	s.costLedger.Record("paid", "m", 100_000, 100_000) // $1.8

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/dashboard/api/usage?key="+testGatewayToken, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		CostTotal    float64 `json:"cost_total"`
		CostRequests int64   `json:"cost_requests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.CostTotal < 1.79 || body.CostTotal > 1.81 || body.CostRequests != 1 {
		t.Fatalf("usage cost = %+v, want ~1.8/1", body)
	}
}
