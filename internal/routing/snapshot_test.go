package routing

import "testing"

// TestBreakersSnapshotExportsStates 验证 读口：熔断器三种状态导出
// 为字符串行（closed/open/half-open），只读不推进。
func TestBreakersSnapshotExportsStates(t *testing.T) {
	iso, fc := newTestIsolation(t)

	// 无活动：空表。
	if rows := iso.Breakers(); len(rows) != 0 {
		t.Fatalf("fresh Breakers = %v, want empty", rows)
	}

	// 5 连续 5xx（窗口 5/5 ≥ 阈值 5 且失败率 100%）→ open。
	for i := 0; i < 5; i++ {
		iso.ApplyFailure("a", "m", KindUpstream5xx)
	}
	rows := iso.Breakers()
	if len(rows) != 1 || rows[0].Provider != "a" || rows[0].State != "open" {
		t.Fatalf("rows = %+v, want [a open]", rows)
	}
	if rows[0].Failures != 5 {
		t.Errorf("failures = %d, want 5", rows[0].Failures)
	}
	if rows[0].OpenTill == nil || !rows[0].OpenTill.After(fc.Now()) {
		t.Errorf("open_till = %v, want future", rows[0].OpenTill)
	}

	// 冷却中的 b（连接层，非熔断）不出现在 breakers 段。
	iso.ApplyFailure("b", "m", KindRateLimit)
	rows = iso.Breakers()
	if len(rows) != 1 {
		t.Fatalf("cooldown-only provider must not appear in breakers: %+v", rows)
	}

	// 退避到点后 Block() 放行探测（open→half-open），快照读 half-open。
	fc.Add(breakerOpenBase)
	if blocked, _ := iso.Block("a"); blocked {
		t.Fatal("backoff elapsed, probe must be admitted")
	}
	rows = iso.Breakers()
	if len(rows) != 1 || rows[0].State != "half-open" {
		t.Fatalf("rows = %+v, want [a half-open]", rows)
	}

	// 探测成功 → closed，OpenTill 清空。
	iso.ApplySuccess("a")
	rows = iso.Breakers()
	if len(rows) != 1 || rows[0].State != "closed" || rows[0].OpenTill != nil {
		t.Fatalf("rows = %+v, want [a closed, no open_till]", rows)
	}
}
