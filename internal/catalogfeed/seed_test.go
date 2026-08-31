package catalogfeed

import "testing"

// TestSeedFeedParses 内置种子必须随分发即合法（结构校验与正式 feed 同
// 入口）——种子损坏属于发布事故，本测试是门禁。
func TestSeedFeedParses(t *testing.T) {
	f, err := SeedFeed()
	if err != nil {
		t.Fatalf("SeedFeed: %v", err)
	}
	if f.Version < 1 {
		t.Errorf("seed version = %d, want >= 1", f.Version)
	}
	n := 0
	for _, pf := range f.Providers {
		if len(pf.Models) == 0 {
			t.Error("seed provider without models")
		}
		n += len(pf.Models)
	}
	if n < 10 {
		t.Errorf("seed models = %d, want a meaningful bootstrap set (>=10)", n)
	}
}
