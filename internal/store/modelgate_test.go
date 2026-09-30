// modelgate_test.go 锁死模型启停契约：meta 往返（禁用集落库重启
// 恢复）、空集清除、DisabledModels 拷贝隔离。
package store

import "testing"

func TestModelGateRoundTrip(t *testing.T) {
	st, _ := newTestStore(t)
	g1, err := NewModelGateStore(st)
	if err != nil {
		t.Fatalf("NewModelGateStore: %v", err)
	}
	if g1.DisabledModels("zhipu") != nil {
		t.Fatal("fresh store must have no disabled sets")
	}
	if err := g1.SetDisabled("zhipu", []string{"glm-4.7-flash", "glm-4.5-air"}); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}
	dis := g1.DisabledModels("zhipu")
	if !dis["glm-4.7-flash"] || !dis["glm-4.5-air"] || len(dis) != 2 {
		t.Fatalf("disabled = %v", dis)
	}
	// 拷贝隔离：改返回值不得影响缓存。
	dis["x"] = true
	if g1.DisabledModels("zhipu")["x"] {
		t.Fatal("DisabledModels must return a copy")
	}
	// 新实例（模拟重启）从 meta 恢复。
	g2, err := NewModelGateStore(st)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !g2.DisabledModels("zhipu")["glm-4.7-flash"] {
		t.Fatal("disabled set must survive restart")
	}
	// 空集=清除（回到全启用）。
	if err := g2.SetDisabled("zhipu", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if g2.DisabledModels("zhipu") != nil {
		t.Fatal("cleared set must be nil")
	}
	if g3, _ := NewModelGateStore(st); g3.DisabledModels("zhipu") != nil {
		t.Fatal("clear must persist")
	}
}
