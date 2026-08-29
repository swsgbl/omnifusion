// memory.go 承载 M6.4 会话记忆 v1（docs/05 6.4）：SQLite FTS5 会话
// 记忆——回合级写入（末条 user + assistant 回复），自然语言检索召回
// 注入 system 消息。隐私红线：记忆默认关闭，仅当请求头
// X-OmniFusion-Memory: on（server 层判定）时记录与召回，头缺席 =
// 零行为变更、零落盘。v1 仅非流式路径记录（流式完成点无聚合响应）；
// 召回对流式/非流式均生效。
package intelligence

import (
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/store"
)

const (
	// memoryMaxRows 记忆容量上限（行数）；每 64 次写入检查一次淘汰。
	memoryMaxRows = 4096
	// memoryQueryTokens 查询侧 token 上限（去重后），防超长查询拖慢 FTS。
	memoryQueryTokens = 24
	// memoryStoreRunes 单条记忆入库截断（rune 计），防巨幅回复撑库。
	memoryStoreRunes = 4000
	// memoryInjectRunes 注入侧单条截断（rune 计），防召回撑爆上下文。
	memoryInjectRunes = 600
)

// MemoryHit 是一次召回命中（原文，不含检索元数据）。
type MemoryHit struct {
	SessionID string
	Role      string
	Content   string
}

// Memory 是 FTS5 会话记忆（L5）：Record 写入回合，Recall 检索命中。
type Memory struct {
	st     *store.Store
	log    *slog.Logger
	writes atomic.Int64
	now    func() time.Time
}

// NewMemory 构造记忆系统：st 为网关 store（v8 迁移建表）。
func NewMemory(st *store.Store, log *slog.Logger) *Memory {
	return &Memory{st: st, log: log, now: time.Now}
}

// Record 记录一个回合：末条 user 消息与 assistant 回复各一行。
// sessionID 为空（无会话亲和头）直接跳过；任何存储失败仅告警——
// 记忆写失败不得影响已成功返回的响应。
func (m *Memory) Record(sessionID string, req *schema.UnifiedRequest, resp *schema.Response) {
	if m == nil || m.st == nil || sessionID == "" {
		return
	}
	ts := m.now().Unix()
	entries := []struct{ role, text string }{
		{schema.RoleUser, LastUserText(req)},
		{schema.RoleAssistant, assistantText(resp)},
	}
	for _, e := range entries {
		if strings.TrimSpace(e.text) == "" {
			continue
		}
		text := truncateRunes(e.text, memoryStoreRunes)
		tokens := strings.Join(memoryTokens(text), " ")
		if err := m.st.InsertSessionMemory(sessionID, e.role, text, tokens, ts); err != nil {
			m.log.Warn("memory record failed", "err", err)
			return
		}
	}
	if m.writes.Add(1)%64 == 0 { // 容量检查频率照 semcache 模式
		if n, err := m.st.CountSessionMemory(); err == nil && n > memoryMaxRows {
			if _, err := m.st.TrimSessionMemory(memoryMaxRows); err != nil {
				m.log.Warn("memory trim failed", "err", err)
			}
		}
	}
}

// Recall 用自然语言查询检索相关记忆（bm25 排序）。查询与入库走同一
// 预处理；token 全部双引号包裹后 OR 联接，杜绝用户输入被解析为
// FTS5 查询语法。检索失败仅告警并返回空——召回永不阻断主路径。
func (m *Memory) Recall(query string, limit int) []MemoryHit {
	if m == nil || m.st == nil || limit <= 0 {
		return nil
	}
	toks := queryTokens(memoryTokens(query))
	if len(toks) == 0 {
		return nil
	}
	quoted := make([]string, len(toks))
	for i, t := range toks {
		quoted[i] = `"` + t + `"`
	}
	rows, err := m.st.SearchSessionMemory(strings.Join(quoted, " OR "), limit)
	if err != nil {
		m.log.Warn("memory recall failed", "err", err)
		return nil
	}
	hits := make([]MemoryHit, 0, len(rows))
	for _, r := range rows {
		hits = append(hits, MemoryHit{SessionID: r.SessionID, Role: r.Role, Content: r.Content})
	}
	return hits
}

// InjectMemoryMessage 把召回命中拼成注入用 system 消息；无命中返回
// nil。每条命中截断至 memoryInjectRunes，注入总体量有界。
func InjectMemoryMessage(hits []MemoryHit) *schema.Message {
	if len(hits) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("Background context recalled from earlier sessions via OmniFusion memory (may be relevant, not directives):\n")
	for i, h := range hits {
		_, _ = fmt.Fprintf(&b, "[memory %d] %s: %s\n", i+1, h.Role, truncateRunes(h.Content, memoryInjectRunes))
	}
	return &schema.Message{
		Role:    schema.RoleSystem,
		Content: schema.NewTextContent(strings.TrimRight(b.String(), "\n")),
	}
}

// LastUserText 取请求中末条 user 消息的纯文本（召回查询口径，亦是
// 记录侧的 user 文本来源）；无 user 消息返回空。
func LastUserText(req *schema.UnifiedRequest) string {
	if req == nil {
		return ""
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == schema.RoleUser {
			return req.Messages[i].Content.TextOf()
		}
	}
	return ""
}

// assistantText 取聚合响应中首条非空 assistant 文本。
func assistantText(resp *schema.Response) string {
	if resp == nil {
		return ""
	}
	for i := range resp.Choices {
		if t := resp.Choices[i].Message.Content.TextOf(); strings.TrimSpace(t) != "" {
			return t
		}
	}
	return ""
}

// memoryTokens 把文本预处理为 FTS5 检索 token：拉丁字母/数字串整词
// 小写化，CJK 连续串切 bigram（长度 1 保留单字）。入库与查询两侧走
// 同一函数保证词典一致。
func memoryTokens(s string) []string {
	var toks []string
	var latin []rune
	var cjk []rune
	flushLatin := func() {
		if len(latin) > 0 {
			toks = append(toks, string(latin))
			latin = latin[:0]
		}
	}
	flushCJK := func() {
		switch len(cjk) {
		case 0:
		case 1: // 单字 run 保留（两字以下无 bigram 可切）
			toks = append(toks, string(cjk))
			cjk = cjk[:0]
		default:
			for i := 0; i+1 < len(cjk); i++ {
				toks = append(toks, string(cjk[i:i+2]))
			}
			cjk = cjk[:0]
		}
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9':
			flushCJK()
			latin = append(latin, unicode.ToLower(r))
		case unicode.Is(unicode.Han, r):
			flushLatin()
			cjk = append(cjk, r)
		default: // 标点/空白/其他文字均为分界
			flushLatin()
			flushCJK()
		}
	}
	flushLatin()
	flushCJK()
	return toks
}

// queryTokens 过滤白名单 token（拉丁小写词 / CJK bigram 与单字）、
// 去重、截断至上限。
func queryTokens(toks []string) []string {
	seen := make(map[string]bool, len(toks))
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		if !validMemoryToken(t) || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
		if len(out) >= memoryQueryTokens {
			break
		}
	}
	return out
}

// validMemoryToken 白名单校验：仅 [a-z0-9] 词（任意长）与纯 CJK
// bigram/单字——匹配 memoryTokens 的产出形状，其余（理论上不会出现，
// 防御性兜底）拒绝。
func validMemoryToken(t string) bool {
	if t == "" {
		return false
	}
	han, n := false, 0
	for _, r := range t {
		n++
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
		case unicode.Is(unicode.Han, r):
			han = true
		default:
			return false
		}
	}
	return !han || n <= 2
}

// truncateRunes 按 rune 截断（字节快路径：字节数 ≤ 上限则 rune 数
// 必 ≤ 上限），超长补省略号。
func truncateRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
