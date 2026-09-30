// modelgate.go 是用户侧模型启停的持久化（providers 页可编辑，用户
// 2026-10-01 需求）：每 provider 的禁用模型集存 meta 表（键
// model_disabled:<provider>，JSON 数组；空/缺省=全启用——零迁移）。
// 生产实现 ModelGateStore 实现 routing.ModelGate（cmd/ofd 装配注入
// Router.Gate）；读多写少，读路径带 RWMutex 缓存，写后失效。
package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

const modelDisabledPrefix = "model_disabled:"

// ModelGateStore 是基于 meta 表的禁用集存储。
type ModelGateStore struct {
	st  *Store
	mu  sync.RWMutex
	mem map[string]map[string]bool // provider → disabled set（启动全量载入）
}

// NewModelGateStore 构造并全量载入现有禁用集（meta 键前缀扫描）。
func NewModelGateStore(st *Store) (*ModelGateStore, error) {
	g := &ModelGateStore{st: st, mem: map[string]map[string]bool{}}
	keys, err := st.ListMetaKeys(modelDisabledPrefix)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		provider := strings.TrimPrefix(k, modelDisabledPrefix)
		v, err := st.GetMeta(k)
		if err != nil || v == "" {
			continue
		}
		var ids []string
		if err := json.Unmarshal([]byte(v), &ids); err != nil {
			continue // 坏行视作未禁用（保守）
		}
		set := map[string]bool{}
		for _, id := range ids {
			set[id] = true
		}
		g.mem[provider] = set
	}
	return g, nil
}

// DisabledModels 实现 routing.ModelGate（拷贝返回，防外部改缓存）。
func (g *ModelGateStore) DisabledModels(providerName string) map[string]bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	src := g.mem[providerName]
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]bool, len(src))
	for id := range src {
		out[id] = true
	}
	return out
}

// SetDisabled 覆盖 provider 的禁用集（空集=清除键，回到全启用），
// 落库后更新缓存。
func (g *ModelGateStore) SetDisabled(providerName string, models []string) error {
	key := modelDisabledPrefix + providerName
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(models) == 0 {
		if err := g.st.SetMeta(key, ""); err != nil {
			return fmt.Errorf("clear disabled set: %w", err)
		}
		delete(g.mem, providerName)
		return nil
	}
	b, err := json.Marshal(models)
	if err != nil {
		return err
	}
	if err := g.st.SetMeta(key, string(b)); err != nil {
		return fmt.Errorf("persist disabled set: %w", err)
	}
	set := map[string]bool{}
	for _, id := range models {
		set[id] = true
	}
	g.mem[providerName] = set
	return nil
}
