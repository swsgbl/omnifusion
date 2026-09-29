// tasks.go 是 Tasks 扩展的状态核心（蓝图 Phase 6：MCP 2026-07-28
// 思路——无状态协议核心 + 显式 Task store）。Task 是跨请求存活的
// 异步操作单元（生产者：运维管线/后续 A2A 任务面）；本文件只做
// 确定性状态机：合法迁移、幂等、取消、超时（读时惰性判定）与恢复
// （重启后的 running 僵尸任务判 failed）。协议暴露在 tools_tasks.go
// （MCP 工具面），生产者经 DefaultTasks() 取同一进程级实例。
package agent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// TaskStatus 是任务生命周期状态（终态：completed/failed/canceled/
// timed_out——终态不可再迁移）。
type TaskStatus string

const (
	TaskStatusCreated   TaskStatus = "created"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
	TaskStatusCanceled  TaskStatus = "canceled"
	TaskStatusTimedOut  TaskStatus = "timed_out"
)

// TaskTerminal 报告状态是否终态。
func TaskTerminal(s TaskStatus) bool {
	switch s {
	case TaskStatusCompleted, TaskStatusFailed, TaskStatusCanceled, TaskStatusTimedOut:
		return true
	}
	return false
}

// Task 是一个异步操作单元。Result/Err 是生产者写入的最终证据
// （string；结构化载荷由生产者自行 JSON 编码）。
type Task struct {
	ID             string     `json:"id"`
	Kind           string     `json:"kind"`
	Status         TaskStatus `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	Deadline       time.Time  `json:"deadline,omitempty"` // zero = 无超时
	Result         string     `json:"result,omitempty"`
	Err            string     `json:"error,omitempty"`
	IdempotencyKey string     `json:"idempotency_key,omitempty"`
}

// deadlinePassed 报告活跃任务是否已过截止（零值截止=无超时）。
func (t *Task) deadlinePassed(now time.Time) bool {
	return !t.Deadline.IsZero() && now.After(t.Deadline) &&
		(t.Status == TaskStatusCreated || t.Status == TaskStatusRunning)
}

// TaskStore 是进程内任务存储（互斥锁保护；持久化钩子可选——生产
// 装配在后续切片接 SQLite，接口与 quota.Persister 同模式）。
type TaskStore struct {
	mu      sync.Mutex
	tasks   map[string]*Task
	byKey   map[string]string // kind+idemKey → 任务 ID（幂等索引）
	persist func(Task) error
	now     func() time.Time
	newID   func() string
}

// NewTaskStore 构造空存储。
func NewTaskStore() *TaskStore {
	return &TaskStore{
		tasks: map[string]*Task{},
		byKey: map[string]string{},
		now:   time.Now,
		newID: func() string {
			var b [8]byte
			if _, err := rand.Read(b[:]); err != nil {
				return fmt.Sprintf("t_%d", time.Now().UnixNano())
			}
			return "t_" + hex.EncodeToString(b[:])
		},
	}
}

// SetPersister 注入持久化钩子（每次实际落盘的迁移调用；失败只影响
// 持久层，不阻断内存态——与 Ledger 同纪律）。
func (s *TaskStore) SetPersister(f func(Task) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persist = f
}

// SetClock 注入时钟（测试）。
func (s *TaskStore) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// Create 建任务：timeout<=0 表示无超时。幂等：同 kind+idemKey 存在
// 活跃（非终态）任务时返回该任务且 created=false（不重复建）；终态
// 任务不阻挡新任务（重试语义）。
func (s *TaskStore) Create(kind, idemKey string, timeout time.Duration) (*Task, bool, error) {
	if kind == "" {
		return nil, false, errors.New("task kind must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := kind + "\x00" + idemKey
	if idemKey != "" {
		if id, ok := s.byKey[key]; ok {
			if t := s.liveLocked(id); t != nil {
				return cloneTask(t), false, nil
			}
		}
	}
	now := s.now()
	t := &Task{
		ID: s.newID(), Kind: kind, Status: TaskStatusCreated,
		CreatedAt: now, UpdatedAt: now, IdempotencyKey: idemKey,
	}
	if timeout > 0 {
		t.Deadline = now.Add(timeout)
	}
	s.tasks[t.ID] = t
	if idemKey != "" {
		s.byKey[key] = t.ID
	}
	s.persistLocked(*t)
	return cloneTask(t), true, nil
}

// Get 按 ID 取任务（读时惰性超时：created/running 已过 Deadline 判
// timed_out 并落盘——无后台计时器，确定性可测）。
func (s *TaskStore) Get(id string) (*Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return nil, false
	}
	s.expireLocked(t)
	return cloneTask(t), true
}

// Start 迁移 created→running。
func (s *TaskStore) Start(id string) (*Task, error) {
	return s.transition(id, TaskStatusRunning, "")
}

// Complete 迁移 running→completed（幂等：已 completed 且结果相同
// 返回原任务；结果不同报冲突——最终证据不可篡改）。
func (s *TaskStore) Complete(id, result string) (*Task, error) {
	return s.transition(id, TaskStatusCompleted, result)
}

// Fail 迁移 created/running→failed。
func (s *TaskStore) Fail(id, errMsg string) (*Task, error) {
	return s.transition(id, TaskStatusFailed, errMsg)
}

// Cancel 迁移 created/running→canceled（终态任务不可取消——canceled
// 幂等重放返回原任务，其余终态报错）。
func (s *TaskStore) Cancel(id string) (*Task, error) {
	return s.transition(id, TaskStatusCanceled, "")
}

// transition 是统一迁移面：合法性按白名单判定；同状态同载荷重放
// 幂等回显。
func (s *TaskStore) transition(id string, to TaskStatus, payload string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return nil, fmt.Errorf("task %s not found", id)
	}
	s.expireLocked(t)
	// 幂等重放只认终态（complete/cancel 重试安全）；非终态同状态
	// 迁移（running→running）是非法操作。
	if t.Status == to {
		if !TaskTerminal(to) {
			return nil, fmt.Errorf("task %s: illegal transition %s -> %s", id, t.Status, to)
		}
		if payloadFor(to) == "" || payloadOf(t, to) == payload {
			return cloneTask(t), nil
		}
		return nil, fmt.Errorf("task %s already %s with different %s", id, to, payloadFor(to))
	}
	if !allowed(t.Status, to) {
		return nil, fmt.Errorf("task %s: illegal transition %s -> %s", id, t.Status, to)
	}
	t.Status = to
	t.UpdatedAt = s.now()
	switch to {
	case TaskStatusCompleted:
		t.Result = payload
	case TaskStatusFailed:
		t.Err = payload
	}
	s.persistLocked(*t)
	return cloneTask(t), nil
}

// allowed 是迁移白名单（终态一律不可迁出）。
func allowed(from, to TaskStatus) bool {
	switch from {
	case TaskStatusCreated:
		return to == TaskStatusRunning || to == TaskStatusFailed || to == TaskStatusCanceled
	case TaskStatusRunning:
		return to == TaskStatusCompleted || to == TaskStatusFailed || to == TaskStatusCanceled
	}
	return false
}

// expireLocked 惰性超时（持锁调用）。
func (s *TaskStore) expireLocked(t *Task) {
	if t.deadlinePassed(s.now()) {
		t.Status = TaskStatusTimedOut
		t.UpdatedAt = s.now()
		t.Err = "deadline exceeded"
		s.persistLocked(*t)
	}
}

// LoadFrom 恢复任务快照（重启恢复）：活跃任务先判超时；未超时的
// running 任务生产者已随旧进程消失，判 failed("interrupted by
// restart")——诚实终态优于僵尸；created 任务保留（可被认领）。
// 返回状态被改写的任务数。
func (s *TaskStore) LoadFrom(tasks []Task) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, in := range tasks {
		t := in
		if t.Status == TaskStatusRunning {
			if t.deadlinePassed(s.now()) {
				t.Status, t.Err = TaskStatusTimedOut, "deadline exceeded"
			} else {
				t.Status, t.Err = TaskStatusFailed, "interrupted by restart"
			}
			t.UpdatedAt = s.now()
			n++
		} else if t.Status == TaskStatusCreated && t.deadlinePassed(s.now()) {
			t.Status, t.Err = TaskStatusTimedOut, "deadline exceeded"
			t.UpdatedAt = s.now()
			n++
		}
		s.tasks[t.ID] = &t
		if t.IdempotencyKey != "" && !TaskTerminal(t.Status) {
			s.byKey[t.Kind+"\x00"+t.IdempotencyKey] = t.ID
		}
	}
	return n
}

// liveLocked 返回活跃（非终态）任务的副本；终态/不存在返回 nil。
func (s *TaskStore) liveLocked(id string) *Task {
	t, ok := s.tasks[id]
	if !ok || TaskTerminal(t.Status) {
		return nil
	}
	return cloneTask(t)
}

func (s *TaskStore) persistLocked(t Task) {
	if s.persist != nil {
		_ = s.persist(t) // 持久层失败不阻断内存态
	}
}

func payloadFor(to TaskStatus) string {
	switch to {
	case TaskStatusCompleted:
		return "result"
	case TaskStatusFailed:
		return "error"
	}
	return ""
}

func payloadOf(t *Task, to TaskStatus) string {
	switch to {
	case TaskStatusCompleted:
		return t.Result
	case TaskStatusFailed:
		return t.Err
	}
	return ""
}

func cloneTask(t *Task) *Task {
	c := *t
	return &c
}

// defaultTasks 是进程级任务存储（MCP 工具面与进程内生产者共用）。
var defaultTasks = NewTaskStore()

// DefaultTasks 返回进程级任务存储。
func DefaultTasks() *TaskStore { return defaultTasks }
