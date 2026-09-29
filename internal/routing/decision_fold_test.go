package routing

import "testing"

// FoldDecision 与 Summary 的契约测试：既有 Dispatch 调用方（四协议
// 端点）用这两个函数获得决策证据，形状必须稳定。

func TestFoldDecisionFromAttempts(t *testing.T) {
	atts := []Attempt{
		{Provider: "alpha", Model: "m1", Err: errOf("429"), Kind: KindRateLimit},
		{Provider: "beta", Model: "m2", SkipReason: "cooldown until 2026-09-25T12:00:00Z"},
		{Provider: "gamma", Model: "m3"},
	}
	d := FoldDecision(atts, nil, "@quality", "req-42", ReasonQualityAuto, 123, false)
	if !d.Success || d.ChosenProvider != "gamma" || d.ResolvedModel != "m3" {
		t.Fatalf("decision = %+v", d)
	}
	if d.RequestID != "req-42" || d.CandidateSource != ReasonQualityAuto || d.DurationMS != 123 {
		t.Errorf("scalars = %+v", d)
	}
	if len(d.Candidates) != 3 {
		t.Fatalf("candidates = %+v", d.Candidates)
	}
	if d.Candidates[0].Kind != string(KindRateLimit) || d.Candidates[0].Skipped {
		t.Errorf("c0 = %+v", d.Candidates[0])
	}
	if !d.Candidates[1].Skipped || d.Candidates[1].SkipReason != ReasonCircuitOpen {
		t.Errorf("c1 = %+v", d.Candidates[1])
	}
	if d.Candidates[2].Kind != "" {
		t.Errorf("c2 (winner) kind must be empty: %+v", d.Candidates[2])
	}
	if d.ReasonCodes[0] != ReasonFailoverChosen {
		t.Errorf("reasons = %v", d.ReasonCodes)
	}
}

func TestFoldDecisionExhausted(t *testing.T) {
	atts := []Attempt{{Provider: "alpha", Err: errOf("500"), Kind: KindUpstream5xx}}
	d := FoldDecision(atts, errOf("all failed"), "m", "", ReasonDirectModel, 5, false)
	if d.Success || d.ChosenProvider != "" {
		t.Fatalf("decision = %+v", d)
	}
	if d.ReasonCodes[0] != ReasonAllExhausted {
		t.Errorf("reasons = %v", d.ReasonCodes)
	}
}

func TestSummary(t *testing.T) {
	d := FoldDecision([]Attempt{
		{Provider: "alpha", Err: errOf("429"), Kind: KindRateLimit},
		{Provider: "beta", Model: "m2"},
	}, nil, "m", "", ReasonDirectModel, 50, false)
	got := d.Summary()
	want := "chosen=beta reason=FAILOVER_CHOSEN tries=2 path=alpha(rate_limit)→beta"
	if got != want {
		t.Errorf("Summary() =\n  %s\nwant\n  %s", got, want)
	}
	// nil 安全
	var nilD *RouteDecision
	if nilD.Summary() != "" {
		t.Error("nil Summary must be empty")
	}
}
