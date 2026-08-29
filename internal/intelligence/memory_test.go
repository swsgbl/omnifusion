package intelligence

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/store"
)

func testMemory(t *testing.T) *Memory {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return NewMemory(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func userReq(text string) *schema.UnifiedRequest {
	return &schema.UnifiedRequest{Messages: []schema.Message{
		{Role: schema.RoleUser, Content: schema.NewTextContent(text)},
	}}
}

func assistResp(text string) *schema.Response {
	return &schema.Response{Choices: []schema.ResponseChoice{{
		Message: schema.Message{Role: schema.RoleAssistant, Content: schema.NewTextContent(text)},
	}}}
}

// TestMemoryTokens 预处理口径：拉丁整词小写化、CJK 连续串切 bigram、
// 单字 CJK run 保留、标点/空白分界。
func TestMemoryTokens(t *testing.T) {
	cases := []struct {
		in   string
		want string // 空格串联；空输入 → 空串
	}{
		{"", ""},
		{"Hello, FTS5 世界!", "hello fts5 世界"},
		{"网关部署", "网关 关部 部署"},
		{"网", "网"},                                   // 单字 run 保留
		{"deploy-the gateway", "deploy the gateway"}, // 连字符分界
		{"A B  a", "a b a"},                          // 大小写归一但不去重（去重在查询侧）
		{"user123 你好", "user123 你好"},
	}
	for _, c := range cases {
		if got := strings.Join(memoryTokens(c.in), " "); got != c.want {
			t.Errorf("memoryTokens(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

// TestQueryTokensSanitize 白名单过滤 + 去重 + 上限。
func TestQueryTokensSanitize(t *testing.T) {
	in := []string{"hello", `he"llo`, "hello", "世界", "网", "OK", "", "a b"}
	got := queryTokens(in)
	want := []string{"hello", "世界", "网"}
	if len(got) != len(want) {
		t.Fatalf("queryTokens = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("queryTokens = %v; want %v", got, want)
		}
	}
	// 上限：重复词不占额、超限截断
	big := make([]string, 64)
	for i := range big {
		big[i] = "w" + strings.Repeat("x", i) // 互异且合法
	}
	if n := len(queryTokens(big)); n != memoryQueryTokens {
		t.Fatalf("上限截断 = %d; want %d", n, memoryQueryTokens)
	}
}

// TestMemoryRecordAndRecall 回合记录（user+assistant 双行）与中英混合
// 检索；空 session 跳过。
func TestMemoryRecordAndRecall(t *testing.T) {
	m := testMemory(t)
	m.Record("s1", userReq("网关部署方案用什么"), assistResp("用 SQLite FTS5 做会话记忆"))

	st := m.st
	if n, err := st.CountSessionMemory(); err != nil || n != 2 {
		t.Fatalf("记录行数 = %d, %v; want 2（user+assistant）", n, err)
	}

	// 中文词命中 user 行（bigram 词「网关」在查询与入库两侧同口径）
	hits := m.Recall("网关 部署", 4)
	if len(hits) == 0 {
		t.Fatal("中文召回空")
	}
	found := false
	for _, h := range hits {
		if h.Role == schema.RoleUser && strings.Contains(h.Content, "网关部署方案") {
			found = true
		}
	}
	if !found {
		t.Fatalf("user 行未被召回: %+v", hits)
	}

	// 英文词命中 assistant 行（大小写归一：FTS5 → fts5）
	hits = m.Recall("sqlite fts5", 4)
	if len(hits) == 0 || hits[0].Role != schema.RoleAssistant ||
		!strings.Contains(hits[0].Content, "SQLite") {
		t.Fatalf("英文召回 = %+v", hits)
	}

	// 空 session：不落盘
	m.Record("", userReq("another"), assistResp("reply"))
	if n, err := st.CountSessionMemory(); err != nil || n != 2 {
		t.Fatalf("空 session 后行数 = %d, %v; want 2", n, err)
	}

	// 无关查询与空查询：空结果不报错
	if hits := m.Recall("zzzqqq", 4); len(hits) != 0 {
		t.Fatalf("无关查询 = %+v", hits)
	}
	if hits := m.Recall("   ", 4); len(hits) != 0 {
		t.Fatalf("空查询 = %+v", hits)
	}
}

// TestMemoryRecallLimit limit 生效；limit<=0 返回空。
func TestMemoryRecallLimit(t *testing.T) {
	m := testMemory(t)
	for i := 0; i < 3; i++ {
		m.Record("s", userReq("alpha topic "+strings.Repeat("好", i+1)),
			assistResp("beta"))
	}
	if hits := m.Recall("alpha", 2); len(hits) != 2 {
		t.Fatalf("limit=2 召回 %d 行; want 2", len(hits))
	}
	if hits := m.Recall("alpha", 0); len(hits) != 0 {
		t.Fatalf("limit=0 召回 %d 行; want 0", len(hits))
	}
}

// TestMemoryRecallHostileQuery 恶意/特殊字符查询不报错不 panic——
// 全部经白名单 token 化 + 双引号包裹，FTS5 语法注入不可能。
func TestMemoryRecallHostileQuery(t *testing.T) {
	m := testMemory(t)
	m.Record("s1", userReq("normal content here"), assistResp("fine"))
	for _, q := range []string{
		`" OR 1=1 --`, `NEAR(a b)`, `*`, `col:x`, `^boot`, `{}${jndi:ldap://x}`, `"`,
	} {
		if hits := m.Recall(q, 4); len(hits) != 0 && !allSane(hits) {
			t.Fatalf("恶意查询 %q 产出异常命中: %+v", q, hits)
		}
	}
}

func allSane(hits []MemoryHit) bool {
	for _, h := range hits {
		if h.SessionID == "" || h.Content == "" {
			return false
		}
	}
	return true
}

// TestInjectMemoryMessage 注入消息格式：编号行、role 前缀、首行说明；
// 空命中返回 nil；单条超长截断。
func TestInjectMemoryMessage(t *testing.T) {
	if InjectMemoryMessage(nil) != nil {
		t.Fatal("空命中应返回 nil")
	}
	msg := InjectMemoryMessage([]MemoryHit{
		{SessionID: "s1", Role: "user", Content: "问了个问题"},
		{SessionID: "s1", Role: "assistant", Content: strings.Repeat("长", memoryInjectRunes+50)},
	})
	if msg == nil || msg.Role != schema.RoleSystem {
		t.Fatalf("消息 = %+v; want system", msg)
	}
	text := msg.Content.TextOf()
	if !strings.Contains(text, "[memory 1] user: 问了个问题") {
		t.Fatalf("缺少编号 user 行: %q", text)
	}
	if !strings.Contains(text, "[memory 2] assistant: ") {
		t.Fatalf("缺少编号 assistant 行: %q", text)
	}
	if !strings.HasPrefix(text, "Background context") {
		t.Fatalf("缺少首行说明: %q", text)
	}
	line2 := strings.SplitN(text, "\n", 3)[2] // 第 2 条记忆行
	if strings.Count(line2, "长") > memoryInjectRunes {
		t.Fatalf("注入行未截断: %d 个「长」> %d", strings.Count(line2, "长"), memoryInjectRunes)
	}
}

// TestLastUserText 取末条 user 消息（跳过 assistant/tool）。
func TestLastUserText(t *testing.T) {
	req := &schema.UnifiedRequest{Messages: []schema.Message{
		{Role: schema.RoleSystem, Content: schema.NewTextContent("sys")},
		{Role: schema.RoleUser, Content: schema.NewTextContent("first")},
		{Role: schema.RoleAssistant, Content: schema.NewTextContent("mid")},
		{Role: schema.RoleUser, Content: schema.NewTextContent("second")},
	}}
	if got := LastUserText(req); got != "second" {
		t.Fatalf("LastUserText = %q; want second", got)
	}
	if got := LastUserText(nil); got != "" {
		t.Fatalf("nil 请求 = %q; want 空", got)
	}
}

// TestMemoryRecordTruncation 入库截断：超长内容按 rune 截断补省略号。
func TestMemoryRecordTruncation(t *testing.T) {
	m := testMemory(t)
	long := "marker " + strings.Repeat("佳", memoryStoreRunes+100)
	m.Record("s1", userReq(long), assistResp("ok"))
	rows, err := m.st.SearchSessionMemory(`"marker"`, 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("截断行不可检索 = %v, %v", rows, err)
	}
	if n := len([]rune(rows[0].Content)); n != memoryStoreRunes+1 {
		t.Fatalf("入库长度 = %d runes; want %d+1（截断+省略号）", n, memoryStoreRunes)
	}
}
