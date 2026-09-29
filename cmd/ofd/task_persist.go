// task_persist.go 是任务存储持久化的装配适配器（与 quota_persist.go
// 同模式）：store 是依赖叶子（不 import agent，depguard 门禁），
// agent 侧 TaskStore 只认 persist 钩子——类型转换在本层完成。
package main

import (
	"log/slog"

	"github.com/swsgbl/omnifusion/internal/agent"
	"github.com/swsgbl/omnifusion/internal/store"
)

// wireTasks 给任务存储接上 SQLite：先恢复（LoadFrom——僵尸 running
// 判 failed、过期判 timed_out），再挂 persist 钩子（后续每次状态迁移
// 自动落库）。恢复/落库失败只降级为内存态（任务面永不阻断网关启动）。
// ts 显式注入（main 传 agent.DefaultTasks()；测试传独立实例）。
func wireTasks(ts *agent.TaskStore, st *store.Store, log *slog.Logger) {
	if rows, err := st.LoadTasks(); err != nil {
		if log != nil {
			log.Warn("restore tasks; starting with empty task store", "err", err)
		}
	} else if len(rows) > 0 {
		tasks := make([]agent.Task, 0, len(rows))
		for _, r := range rows {
			tasks = append(tasks, agent.Task{
				ID: r.ID, Kind: r.Kind, Status: agent.TaskStatus(r.Status),
				CreatedAt: store.ParseRFC3339(r.CreatedAt),
				UpdatedAt: store.ParseRFC3339(r.UpdatedAt),
				Deadline:  store.ParseRFC3339(r.Deadline),
				Result:    r.Result, Err: r.Error, IdempotencyKey: r.IdempotencyKey,
			})
		}
		rewritten := ts.LoadFrom(tasks)
		if log != nil {
			log.Info("task store restored", "tasks", len(tasks), "rewritten", rewritten)
		}
	}
	ts.SetPersister(func(t agent.Task) error {
		row := store.TaskRow{
			ID: t.ID, Kind: t.Kind, Status: string(t.Status),
			CreatedAt: store.FormatRFC3339(t.CreatedAt),
			UpdatedAt: store.FormatRFC3339(t.UpdatedAt),
			Deadline:  store.FormatRFC3339(t.Deadline),
			Result:    t.Result, Error: t.Err, IdempotencyKey: t.IdempotencyKey,
		}
		if err := st.UpsertTask(row); err != nil {
			if log != nil {
				log.Warn("persist task failed", "id", t.ID, "err", err)
			}
			return err
		}
		return nil
	})
}
