// statuscmd.go 实现 `ofd status`（M2.6）：provider/key/model 三层健康
// 视图。离线快照——provider 层读 registry 与凭据可达性，key 层读
// connections 表（含 env 回退），model 层读 SQLite 持久化的冷却/锁定；
// 网关进程内的熔断计数/配额窗口/打分是内存态，跨进程不可见（脚注注明）。
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/swsgbl/omnifusion/internal/config"
	"github.com/swsgbl/omnifusion/internal/provider/registry"
	"github.com/swsgbl/omnifusion/internal/store"
)

// providerStatus 是单 provider 的展示行（纯数据，便于测试）。
type providerStatus struct {
	ID        string
	Key       string // stored | stored(label) | env:VAR | none | -
	State     string // ready | no-key | missing <var>
	Quota     string // "rpm=30 rpd=14400" / "-"
	Isolation string // 活跃冷却/锁定摘要，多个分号连接；无则 "-"
	Ready     bool
}

// buildProviderStatuses 组装三层视图，不碰 IO（env 经注入）。
func buildProviderStatuses(
	entries []registry.Entry,
	conns map[string]store.Connection,
	env func(string) string,
	cds []store.Cooldown,
	now time.Time,
) []providerStatus {
	byProvider := map[string][]store.Cooldown{}
	for _, c := range cds {
		byProvider[c.Provider] = append(byProvider[c.Provider], c)
	}

	out := make([]providerStatus, 0, len(entries))
	for _, e := range entries {
		ps := providerStatus{ID: e.ID, Quota: quotaSummary(e.RateLimits)}

		switch {
		case conns[e.ID].Provider != "":
			ps.Key = "stored"
			if l := conns[e.ID].Label; l != "" {
				ps.Key += "(" + l + ")"
			}
		case e.KeyEnv != "" && env(e.KeyEnv) != "":
			ps.Key = "env:" + e.KeyEnv
		case e.OptionalKey:
			ps.Key = "-"
		default:
			ps.Key = "none"
		}

		missing := missingURLVars(e, env)
		switch {
		case ps.Key == "none":
			ps.State = "no-key"
		case missing != "":
			ps.State = "missing " + missing
		default:
			ps.State, ps.Ready = "ready", true
		}

		if iso := isolationSummary(byProvider[e.ID], now); iso != "" {
			ps.Isolation = iso
			if ps.State == "ready" { // 冷却中的 provider 仍算已配置，但非首选
				ps.State = "cooldown"
			}
		} else {
			ps.Isolation = "-"
		}
		out = append(out, ps)
	}
	return out
}

// missingURLVars 返回第一个取不到环境值的 URL 变量（如 cloudflare 的
// account_id）；全部满足返回空。
func missingURLVars(e registry.Entry, env func(string) string) string {
	for _, v := range e.URLVars {
		if envName, ok := e.VarsEnv[v]; !ok || env(envName) == "" {
			return v
		}
	}
	return ""
}

func quotaSummary(l registry.RateLimitsDecl) string {
	var parts []string
	if l.RPM > 0 {
		parts = append(parts, fmt.Sprintf("rpm=%d", l.RPM))
	}
	if l.RPD > 0 {
		parts = append(parts, fmt.Sprintf("rpd=%d", l.RPD))
	}
	if l.TPM > 0 {
		parts = append(parts, fmt.Sprintf("tpm=%d", l.TPM))
	}
	if l.TPD > 0 {
		parts = append(parts, fmt.Sprintf("tpd=%d", l.TPD))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

// isolationSummary 汇总单 provider 的活跃隔离：connection 冷却一行，
// model 锁定逐模型一行。
func isolationSummary(cds []store.Cooldown, now time.Time) string {
	sort.Slice(cds, func(i, j int) bool { return cds[i].Until.Before(cds[j].Until) })
	var parts []string
	for _, c := range cds {
		left := c.Until.Sub(now).Round(time.Second)
		if left < 0 {
			left = 0
		}
		if c.ScopeType == "model" {
			parts = append(parts, fmt.Sprintf("model %s locked %s (%s)", c.Model, left, c.Reason))
			continue
		}
		parts = append(parts, fmt.Sprintf("cooldown %s (%s)", left, c.Reason))
	}
	return strings.Join(parts, "; ")
}

// renderStatus 输出终端表格与统计行。
func renderStatus(w io.Writer, stats []providerStatus, dataPath string) {
	ready := 0
	isolated := 0
	for _, ps := range stats {
		if ps.Ready {
			ready++
		}
		if ps.Isolation != "-" {
			isolated++
		}
	}
	_, _ = fmt.Fprintf(w, "OmniFusion status — %d/%d providers ready, %d under isolation (data: %s)\n\n",
		ready, len(stats), isolated, dataPath)
	_, _ = fmt.Fprintf(w, "%-12s %-26s %-18s %-30s %s\n", "PROVIDER", "KEY", "STATE", "QUOTA(free tier)", "ISOLATION")
	for _, ps := range stats {
		_, _ = fmt.Fprintf(w, "%-12s %-26s %-18s %-30s %s\n", ps.ID, ps.Key, ps.State, ps.Quota, ps.Isolation)
	}
	_, _ = fmt.Fprintln(w, "\nnote: breaker/quota-window/scoring state lives in the running gateway process and is not shown here")
}

// runStatusCommand 装配真实数据源并渲染（ofd status）。
func runStatusCommand(cfg *config.Config) error {
	entries, err := registry.Load()
	if err != nil {
		return fmt.Errorf("load provider registry: %w", err)
	}

	if dir := filepath.Dir(cfg.Store.Path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create data dir: %w", err)
		}
	}
	st, err := store.Open(cfg.Store.Path)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	conns := map[string]store.Connection{}
	for _, c := range mustListConnections(st) {
		conns[c.Provider] = c
	}
	now := time.Now()
	stats := buildProviderStatuses(entries, conns, os.Getenv, mustLoadCooldowns(st, now), now)
	renderStatus(os.Stdout, stats, cfg.Store.Path)
	return nil
}

func mustListConnections(st *store.Store) []store.Connection {
	conns, err := st.ListConnections()
	if err != nil {
		return nil // 状态视图尽力而为：读不到按空处理
	}
	return conns
}

func mustLoadCooldowns(st *store.Store, now time.Time) []store.Cooldown {
	cds, err := st.LoadCooldowns(now)
	if err != nil {
		return nil
	}
	return cds
}
