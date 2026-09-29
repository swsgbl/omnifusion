// task_persist_test.go 验证任务存储的重启存活闭环（蓝图 Phase 6
// 持久化切片门禁场景）：迁移落库 → 新进程（新 TaskStore + 新连接）
// 恢复 → 状态/结果/幂等键原样、僵尸 running 判 failed、完成态不可
// 篡改。与权益账本的重启存活测试同模式。
package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/agent"
	"github.com/swsgbl/omnifusion/internal/store"
)

func TestTaskPersistRestartSurvival(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tasks.db")

	// 第一段：建任务、推进到 running、另一个完成——persist 钩子自动落库。
	st1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store (run 1): %v", err)
	}
	ts1 := agent.NewTaskStore()
	wireTasks(ts1, st1, nil)

	running, created, err := ts1.Create("ops", "job-42", 0)
	if err != nil || !created {
		t.Fatalf("create running task: %v", err)
	}
	if _, err := ts1.Start(running.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	done, _, err := ts1.Create("ops", "", time.Minute)
	if err != nil {
		t.Fatalf("create done task: %v", err)
	}
	if _, err := ts1.Start(done.ID); err != nil {
		t.Fatalf("start done: %v", err)
	}
	if _, err := ts1.Complete(done.ID, `{"lines":3}`); err != nil {
		t.Fatalf("complete: %v", err)
	}
	// 活跃 created 任务（未过期）——重启后应保留且幂等键仍挡重复创建。
	active, _, err := ts1.Create("ops", "still-active", 0)
	if err != nil {
		t.Fatalf("create active task: %v", err)
	}
	if err := st1.Close(); err != nil {
		t.Fatalf("close store 1: %v", err)
	}

	// 第二段：模拟重启——新连接、新 TaskStore、同一库文件。
	st2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store (run 2): %v", err)
	}
	defer func() { _ = st2.Close() }()
	ts2 := agent.NewTaskStore()
	wireTasks(ts2, st2, nil)

	zombie, ok := ts2.Get(running.ID)
	if !ok {
		t.Fatalf("running task did not survive restart")
	}
	if zombie.Status != agent.TaskStatusFailed || zombie.Err != "interrupted by restart" {
		t.Fatalf("zombie running must fail honestly after restart: %+v", zombie)
	}
	if zombie.IdempotencyKey != "job-42" {
		t.Fatalf("idempotency key lost: %+v", zombie)
	}

	final, ok := ts2.Get(done.ID)
	if !ok || final.Status != agent.TaskStatusCompleted || final.Result != `{"lines":3}` {
		t.Fatalf("completed task must survive restart untouched: %+v ok=%v", final, ok)
	}
	// 恢复后的改写也落库：complete 重放幂等、篡改被拒。
	if _, err := ts2.Complete(final.ID, `{"lines":999}`); err == nil {
		t.Fatalf("tampering with persisted result must be rejected")
	}
	// 幂等键恢复：活跃 created 任务挡住重复创建（不建新任务）。
	if got, createdNow, err := ts2.Create("ops", "still-active", 0); err != nil || createdNow || got.ID != active.ID {
		t.Fatalf("active idempotency key must survive restart: got=%s created=%v err=%v", got.ID, createdNow, err)
	}
}
