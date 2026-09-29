// tasks_test.go 锁死 tasks 表契约：行往返（RFC3339 时间列）、upsert
// 整行替换、CHECK 约束拒绝非法状态（漂移在库层被拦截）。
package store

import (
	"testing"
	"time"
)

func TestTaskRowRoundTrip(t *testing.T) {
	st, _ := newTestStore(t)
	created := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	in := TaskRow{
		ID: "t_deadbeef", Kind: "ops", Status: "completed",
		CreatedAt: FormatRFC3339(created), UpdatedAt: FormatRFC3339(created.Add(time.Minute)),
		Deadline: FormatRFC3339(created.Add(time.Hour)),
		Result:   `{"ok":true}`, Error: "", IdempotencyKey: "job-1",
	}
	if err := st.UpsertTask(in); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	rows, err := st.LoadTasks()
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	got := rows[0]
	if got != in {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, in)
	}
	if ParseRFC3339(got.CreatedAt) != created {
		t.Fatalf("created_at time did not survive RFC3339 round trip")
	}
}

func TestTaskUpsertReplaces(t *testing.T) {
	st, _ := newTestStore(t)
	first := TaskRow{ID: "t_1", Kind: "ops", Status: "created"}
	if err := st.UpsertTask(first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := st.UpsertTask(TaskRow{ID: "t_1", Kind: "ops", Status: "running"}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	rows, _ := st.LoadTasks()
	if len(rows) != 1 || rows[0].Status != "running" {
		t.Fatalf("upsert must replace in place, got %+v", rows)
	}
}

func TestTaskStatusCheckConstraint(t *testing.T) {
	st, _ := newTestStore(t)
	if err := st.UpsertTask(TaskRow{ID: "t_x", Kind: "ops", Status: "bogus"}); err == nil {
		t.Fatalf("CHECK constraint must reject unknown status")
	}
	if err := st.UpsertTask(TaskRow{ID: "", Kind: "ops", Status: "created"}); err == nil {
		t.Fatalf("empty id must be rejected")
	}
}
