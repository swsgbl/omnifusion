// tasks_test.go 锁死 Tasks 状态核心契约：合法迁移白名单、终态不可
// 迁出、幂等（重放同终态同载荷 no-op / 不同载荷冲突）、取消、惰性
// 超时、重启恢复（僵尸 running 判 failed、过期判 timed_out、created
// 保留）、幂等键去重与终态后重试。
package agent

import (
	"strings"
	"testing"
	"time"
)

func TestTaskLifecycleHappyPath(t *testing.T) {
	s := NewTaskStore()
	base := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return base })
	task, created, err := s.Create("ops", "", time.Minute)
	if err != nil || !created || task.Status != TaskStatusCreated {
		t.Fatalf("Create: %+v %v", task, err)
	}
	if !strings.HasPrefix(task.ID, "t_") {
		t.Fatalf("task id must be t_-prefixed, got %q", task.ID)
	}
	if got, err := s.Start(task.ID); err != nil || got.Status != TaskStatusRunning {
		t.Fatalf("Start: %+v %v", got, err)
	}
	if got, err := s.Complete(task.ID, `{"ok":true}`); err != nil || got.Status != TaskStatusCompleted || got.Result != `{"ok":true}` {
		t.Fatalf("Complete: %+v %v", got, err)
	}
}

func TestTaskIllegalTransitions(t *testing.T) {
	s := NewTaskStore()
	task, _, _ := s.Create("ops", "", 0)

	// created 不能直接 complete（须先 running）。
	if _, err := s.Complete(task.ID, "x"); err == nil {
		t.Fatalf("created -> completed must be illegal")
	}
	if _, err := s.Start(task.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := s.Start(task.ID); err == nil {
		t.Fatalf("running -> running must be illegal")
	}
	if _, err := s.Complete(task.ID, "r"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// 终态不可迁出。
	for _, call := range []struct {
		name string
		fn   func() error
	}{
		{"start after completed", func() error { _, e := s.Start(task.ID); return e }},
		{"fail after completed", func() error { _, e := s.Fail(task.ID, "x"); return e }},
		{"cancel after completed", func() error { _, e := s.Cancel(task.ID); return e }},
	} {
		if err := call.fn(); err == nil {
			t.Fatalf("%s must fail", call.name)
		}
	}
}

func TestTaskIdempotentReplay(t *testing.T) {
	s := NewTaskStore()
	task, _, _ := s.Create("ops", "", 0)
	_, _ = s.Start(task.ID)
	first, err := s.Complete(task.ID, "same-result")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	replay, err := s.Complete(task.ID, "same-result")
	if err != nil {
		t.Fatalf("same-payload replay must be idempotent, got %v", err)
	}
	if replay.Status != TaskStatusCompleted || replay.UpdatedAt != first.UpdatedAt {
		t.Fatalf("replay must return original task untouched")
	}
	if _, err := s.Complete(task.ID, "different-result"); err == nil {
		t.Fatalf("different payload on terminal task must conflict")
	}
	// cancel 幂等重放。
	c, _, _ := s.Create("ops", "", 0)
	if _, err := s.Cancel(c.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, err := s.Cancel(c.ID); err != nil {
		t.Fatalf("cancel replay must be idempotent, got %v", err)
	}
}

func TestTaskIdempotencyKey(t *testing.T) {
	s := NewTaskStore()
	a, created, err := s.Create("ops", "job-1", 0)
	if err != nil || !created {
		t.Fatalf("first create: %v", err)
	}
	b, created, err := s.Create("ops", "job-1", 0)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if created || b.ID != a.ID {
		t.Fatalf("same kind+key while active must reuse task: a=%s b=%s created=%v", a.ID, b.ID, created)
	}
	// 不同 kind 同 key 不去重。
	c, created, _ := s.Create("a2a", "job-1", 0)
	if !created || c.ID == a.ID {
		t.Fatalf("different kind must not dedupe")
	}
	// 终态后同 key 允许新建（重试语义）。
	if _, err := s.Fail(a.ID, "boom"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	d, created, err := s.Create("ops", "job-1", 0)
	if err != nil || !created || d.ID == a.ID {
		t.Fatalf("terminal task must not block new create: %v created=%v", err, created)
	}
}

func TestTaskLazyTimeout(t *testing.T) {
	s := NewTaskStore()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	task, _, _ := s.Create("ops", "", time.Minute)
	_, _ = s.Start(task.ID)

	now = now.Add(2 * time.Minute) // 时钟推进越过截止。
	got, ok := s.Get(task.ID)
	if !ok || got.Status != TaskStatusTimedOut || got.Err != "deadline exceeded" {
		t.Fatalf("lazy timeout on read failed: %+v ok=%v", got, ok)
	}
	// 超时是终态：complete 被拒。
	if _, err := s.Complete(task.ID, "late"); err == nil {
		t.Fatalf("complete after timeout must fail")
	}
	// 无截止任务永不超时。
	immortal, _, _ := s.Create("ops", "", 0)
	now = now.Add(100 * time.Hour)
	if g, _ := s.Get(immortal.ID); g.Status != TaskStatusCreated {
		t.Fatalf("no-deadline task must not time out: %s", g.Status)
	}
}

func TestTaskRecoveryOnLoad(t *testing.T) {
	s := NewTaskStore()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	changed := s.LoadFrom([]Task{
		{ID: "t_run", Kind: "ops", Status: TaskStatusRunning,
			CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute)},
		{ID: "t_run_exp", Kind: "ops", Status: TaskStatusRunning,
			CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour),
			Deadline: now.Add(-time.Hour)},
		{ID: "t_created", Kind: "ops", Status: TaskStatusCreated,
			CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute)},
		{ID: "t_done", Kind: "ops", Status: TaskStatusCompleted, Result: "kept",
			CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)},
	})
	if changed != 2 {
		t.Fatalf("expected 2 rewritten tasks (zombie running + expired running), got %d", changed)
	}
	if g, _ := s.Get("t_run"); g.Status != TaskStatusFailed || g.Err != "interrupted by restart" {
		t.Fatalf("zombie running must fail honestly: %+v", g)
	}
	if g, _ := s.Get("t_run_exp"); g.Status != TaskStatusTimedOut {
		t.Fatalf("expired running must time out: %+v", g)
	}
	if g, _ := s.Get("t_created"); g.Status != TaskStatusCreated {
		t.Fatalf("unexpired created must survive: %+v", g)
	}
	if g, _ := s.Get("t_done"); g.Status != TaskStatusCompleted || g.Result != "kept" {
		t.Fatalf("terminal tasks must load untouched: %+v", g)
	}
}

func TestTaskPersistHook(t *testing.T) {
	s := NewTaskStore()
	var persisted []TaskStatus
	s.SetPersister(func(t Task) error {
		persisted = append(persisted, t.Status)
		return nil
	})
	task, _, _ := s.Create("ops", "", 0)
	_, _ = s.Start(task.ID)
	_, _ = s.Complete(task.ID, "done")
	want := []TaskStatus{TaskStatusCreated, TaskStatusRunning, TaskStatusCompleted}
	if len(persisted) != len(want) {
		t.Fatalf("persist calls = %v, want %v", persisted, want)
	}
	for i, w := range want {
		if persisted[i] != w {
			t.Fatalf("persist[%d] = %s, want %s", i, persisted[i], w)
		}
	}
}

func TestTaskNotFoundAndEmptyKind(t *testing.T) {
	s := NewTaskStore()
	if _, err := s.Start("t_missing"); err == nil {
		t.Fatalf("unknown id must error")
	}
	if _, _, err := s.Create("", "", 0); err == nil {
		t.Fatalf("empty kind must error")
	}
}

func TestTaskGetReturnsCopy(t *testing.T) {
	s := NewTaskStore()
	task, _, _ := s.Create("ops", "", 0)
	got, _ := s.Get(task.ID)
	got.Status = TaskStatusFailed // 改副本不得影响存储。
	if again, _ := s.Get(task.ID); again.Status != TaskStatusCreated {
		t.Fatalf("Get must return a copy, store was mutated")
	}
}
