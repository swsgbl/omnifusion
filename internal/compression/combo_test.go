package compression

import "testing"

func TestBuildCombo(t *testing.T) {
	pipe, err := BuildCombo([]string{"dedup", "caveman"})
	if err != nil {
		t.Fatalf("BuildCombo: %v", err)
	}
	if len(pipe.stages) != 2 {
		t.Errorf("stages = %d, want 2", len(pipe.stages))
	}
	// 注册键（dedup/caveman）映射到阶段 canonical 名（session_dedup/caveman）
	if pipe.stages[0].Name() != "session_dedup" || pipe.stages[1].Name() != "caveman" {
		t.Errorf("stage order = %s, %s; want session_dedup, caveman",
			pipe.stages[0].Name(), pipe.stages[1].Name())
	}
	if pipe.gate == nil {
		t.Error("默认 Fidelity Gate 未装配")
	}
}

func TestBuildComboErrors(t *testing.T) {
	if _, err := BuildCombo(nil); err == nil {
		t.Error("空阶段列表应报错")
	}
	if _, err := BuildCombo([]string{"dedup", "bogus"}); err == nil {
		t.Error("未知名阶段应报错")
	}
}

func TestStageNamesStable(t *testing.T) {
	names := StageNames()
	want := []string{"caveman", "dedup", "semantic", "semantic_sidecar", "toolfilter"}
	if len(names) != len(want) {
		t.Fatalf("StageNames() = %v, want %v", names, want)
	}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("StageNames()[%d] = %q, want %q", i, names[i], w)
		}
	}
}
