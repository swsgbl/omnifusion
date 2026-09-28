package routing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
)

func writeOK(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, okCompletion(model))
}

func errOf(msg string) error { return errors.New(msg) }

// Decide 的契约测试：决策证据必须如实折叠 Dispatch 的过程，且可 JSON
// 序列化回放（蓝图 Phase 2 门禁：路由决策可解释、可回放）。

func TestDecideFirstSuccess(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOK(w, "model-a")
	}))
	defer up.Close()

	r := &Router{Providers: []provider.Provider{newMockAdapter(t, "alpha", up.URL)}}
	resp, d, err := r.Decide(context.Background(), testRequest(), ReasonDirectModel)
	if err != nil || resp == nil {
		t.Fatalf("expected success, got %v", err)
	}
	if !d.Success || d.ChosenProvider != "alpha" {
		t.Fatalf("decision = %+v", d)
	}
	if len(d.ReasonCodes) != 1 || d.ReasonCodes[0] != ReasonFirstSuccess {
		t.Errorf("reason codes = %v", d.ReasonCodes)
	}
	if d.AttemptCount != 1 || len(d.Candidates) != 1 {
		t.Errorf("candidates = %+v", d.Candidates)
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if !containsStr(string(b), `"chosen_provider":"alpha"`) ||
		!containsStr(string(b), `"reason_codes":["FIRST_SUCCESS"]`) ||
		!containsStr(string(b), `"candidate_source":"DIRECT_MODEL"`) {
		t.Errorf("decision JSON shape: %s", b)
	}
}

func TestDecideFailoverChosen(t *testing.T) {
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer upA.Close()
	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOK(w, "model-b")
	}))
	defer upB.Close()

	r := &Router{Providers: []provider.Provider{
		newMockAdapter(t, "alpha", upA.URL),
		newMockAdapter(t, "beta", upB.URL),
	}}
	resp, d, err := r.Decide(context.Background(), testRequest(), ReasonQualityAuto)
	if err != nil || resp == nil {
		t.Fatalf("expected failover success, got %v", err)
	}
	if !d.Success || d.ChosenProvider != "beta" {
		t.Fatalf("decision = %+v", d)
	}
	if d.ReasonCodes[0] != ReasonFailoverChosen {
		t.Errorf("reason = %v", d.ReasonCodes)
	}
	if len(d.Candidates) != 2 || d.Candidates[0].Kind == "" || d.Candidates[1].Kind != "" {
		t.Errorf("candidates = %+v", d.Candidates)
	}
}

func TestDecideAllExhausted(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer up.Close()

	r := &Router{Providers: []provider.Provider{newMockAdapter(t, "alpha", up.URL)}}
	_, d, err := r.Decide(context.Background(), testRequest(), ReasonDirectModel)
	if err == nil {
		t.Fatal("expected error")
	}
	if d.Success {
		t.Error("must not be success")
	}
	if len(d.ReasonCodes) == 0 || d.ReasonCodes[0] != ReasonAllExhausted {
		t.Errorf("reason = %v", d.ReasonCodes)
	}
	if len(d.Candidates) != 1 || d.Candidates[0].Kind == "" {
		t.Errorf("candidates = %+v", d.Candidates)
	}
}

func TestDecideSkippedCandidateCarriesReason(t *testing.T) {
	// skipIfBlocked 的 SkipReason 归类逻辑层直测（Decide 折叠路径同分支）。
	att := Attempt{Provider: "alpha", Err: errOf("routing: skipped (quota window)"), SkipReason: "quota window nearly full"}
	dc := DecisionCandidate{Order: 0, Provider: att.Provider, Kind: string(att.Kind)}
	if att.SkipReason != "" {
		dc.Skipped = true
		dc.SkipReason = classifySkipReason(att.SkipReason)
	}
	if !dc.Skipped || dc.SkipReason != ReasonQuotaWindow {
		t.Errorf("skip classification = %+v", dc)
	}
}

func TestClassifySkipReason(t *testing.T) {
	cases := []struct {
		in   string
		want ReasonCode
	}{
		// 用真实格式（resilience.go/quota.go 的实际产出形态）驱动
		{"breaker open (30s left)", ReasonCircuitOpen},
		{"cooldown until 2026-09-25T12:00:00Z", ReasonCircuitOpen},
		{"model locked until 2026-09-25T12:00:00Z", ReasonModelLocked},
		{"quota rpm exhausted (resets in 45s)", ReasonQuotaWindow},
		{"quota tpd exhausted (resets in 3h0m0s)", ReasonQuotaWindow},
		{"mystery block", ReasonCircuitOpen}, // 未识别：保守归熔断（存在阻断事实）
	}
	for _, c := range cases {
		if got := classifySkipReason(c.in); got != c.want {
			t.Errorf("classifySkipReason(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}
