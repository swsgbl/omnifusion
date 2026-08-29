package store

import "testing"

func TestModelsRoundTrip(t *testing.T) {
	st, _ := newTestStore(t)

	rows := []ModelRow{
		{ID: "m2", CtxLen: 8192},
		{ID: "m1", CtxLen: 4096},
		{ID: ""}, // 空id必须被跳过
	}
	if err := st.ReplaceProviderModels("groq", "free tier: 30 rpm", rows); err != nil {
		t.Fatalf("ReplaceProviderModels: %v", err)
	}
	got, err := st.LoadModels()
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2 (blank id skipped)", len(got))
	}
	// LoadModels 按 (provider, id) 排序
	if got[0].ID != "m1" || got[0].CtxLen != 4096 || got[1].ID != "m2" || got[1].CtxLen != 8192 {
		t.Errorf("rows = %+v", got)
	}
	if got[0].Provider != "groq" || got[0].FreeMeta != "free tier: 30 rpm" {
		t.Errorf("provider/free_meta = %q/%q", got[0].Provider, got[0].FreeMeta)
	}
}

func TestModelsReplaceClearsStale(t *testing.T) {
	st, _ := newTestStore(t)

	if err := st.ReplaceProviderModels("p", "", []ModelRow{{ID: "old-1"}, {ID: "old-2"}}); err != nil {
		t.Fatalf("first replace: %v", err)
	}
	if err := st.ReplaceProviderModels("p", "", []ModelRow{{ID: "new-1"}}); err != nil {
		t.Fatalf("second replace: %v", err)
	}
	got, err := st.LoadModels()
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if len(got) != 1 || got[0].ID != "new-1" {
		t.Fatalf("rows = %+v, want only new-1 (replace is snapshot semantics)", got)
	}
}
