// dispatch_summary_test.go 覆盖全链失败的客户端可见摘要（2026-11-25
// "应用用不了"事故：只回"最后一家 skipped (cooldown)"，看不出是哪把
// key 失效、哪家欠费）与换 key 清隔离。
package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/security"
)

// TestDispatchFailureSummary 逐家列出真实上游失败（provider/model: HTTP
// 状态 类别 + 提示），同家同类只列一次，跳过记录只计数，上游响应体
// 一个字节都不带出去。
func TestDispatchFailureSummary(t *testing.T) {
	leak := "sk-leak-1234567890abcdef"
	attempts := []routing.Attempt{
		{Provider: "zhipu", Model: "glm-5.3", Kind: routing.KindAuthInvalid,
			Err: &provider.UpstreamError{Provider: "zhipu", Status: 401, Body: []byte(`{"key":"` + leak + `"}`)}},
		{Provider: "zhipu", Model: "glm-5.3", Kind: routing.KindUnknown,
			Err: errors.New("routing: model isolated (model locked until later)")}, // 跳过，只计数
		{Provider: "deepseek", Model: "deepseek-v4-pro", Kind: routing.KindQuotaExhausted,
			Err: &provider.UpstreamError{Provider: "deepseek", Status: 402}},
		{Provider: "deepseek", Model: "deepseek-v4-flash", Kind: routing.KindQuotaExhausted,
			Err: &provider.UpstreamError{Provider: "deepseek", Status: 402}}, // 同家同类：不重复列
		{Provider: "nvidia", Model: "deepseek-ai/deepseek-v4-flash-0731", Kind: routing.KindModelGone,
			Err: &provider.UpstreamError{Provider: "nvidia", Status: http.StatusGone}},
		{Provider: "groq", SkipReason: "cooldown until later", Err: errors.New("routing: skipped (cooldown until later)")}, // 跳过
	}
	msg := dispatchFailureSummary(attempts)

	for _, want := range []string{
		"all candidates failed (3 distinct)",
		"zhipu/glm-5.3: HTTP 401 auth_invalid (check the API key",
		"deepseek/deepseek-v4-pro: HTTP 402 quota_exhausted (quota or balance is exhausted)",
		"nvidia/deepseek-ai/deepseek-v4-flash-0731: HTTP 410 model_gone (model was retired",
		"2 other candidate(s) were skipped",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("summary missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "deepseek/deepseek-v4-flash") {
		t.Errorf("same provider/kind failure listed twice:\n%s", msg)
	}
	if strings.Contains(msg, leak) {
		t.Fatalf("upstream body leaked into the client message:\n%s", msg)
	}
}

// TestDispatchFailureSummaryCapsAndEmpty 超过上限的失败折成计数；没有任何
// 失败记录时返回空串让调用方回落旧摘要。
func TestDispatchFailureSummaryCapsAndEmpty(t *testing.T) {
	var many []routing.Attempt
	for i := 0; i < failureSummaryMax+3; i++ {
		p := fmt.Sprintf("p%d", i)
		many = append(many, routing.Attempt{Provider: p, Model: "m", Kind: routing.KindUpstream5xx,
			Err: &provider.UpstreamError{Provider: p, Status: 500}})
	}
	msg := dispatchFailureSummary(many)
	if !strings.Contains(msg, fmt.Sprintf("(%d distinct)", failureSummaryMax+3)) || !strings.Contains(msg, "(+3 more)") {
		t.Errorf("over-cap summary = %q", msg)
	}
	if strings.Contains(msg, "p6") {
		t.Errorf("entries beyond the cap must not be listed: %q", msg)
	}

	if got := dispatchFailureSummary(nil); got != "" {
		t.Errorf("no-attempt summary = %q, want empty", got)
	}
}

// TestDispatchFailureSummarySkipOnly 一个上游都没打、全是跳过（2026-11-25
// 实撞形态：所有候选都在隔离期）：点名谁被隔离、到什么时候，而不是回
// 一句"skipped (cooldown)"。
func TestDispatchFailureSummarySkipOnly(t *testing.T) {
	attempts := []routing.Attempt{
		{Provider: "groq", SkipReason: "cooldown until 2026-11-25T04:46:19Z",
			Err: errors.New("routing: skipped (cooldown until 2026-11-25T04:46:19Z)")},
		{Provider: "zhipu", SkipReason: "cooldown until 2026-11-25T04:46:06Z",
			Err: errors.New("routing: skipped (cooldown until 2026-11-25T04:46:06Z)")},
		{Provider: "groq", SkipReason: "cooldown until 2026-11-25T04:46:19Z",
			Err: errors.New("routing: skipped (cooldown until 2026-11-25T04:46:19Z)")}, // 同家只列一次
		{Provider: "deepseek", Model: "deepseek-v4-flash", Kind: routing.KindUnknown,
			Err: errors.New("routing: model isolated (model locked until 2026-11-26T05:14:18Z)")},
	}
	msg := dispatchFailureSummary(attempts)
	for _, want := range []string{
		"no candidate could be tried",
		"groq (cooldown until 2026-11-25T04:46:19Z)",
		"zhipu (cooldown until 2026-11-25T04:46:06Z)",
		"deepseek/deepseek-v4-flash (model locked until 2026-11-26T05:14:18Z)",
		"fixing the provider (key / balance / network) in the dashboard clears it right away",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("skip-only summary missing %q:\n%s", want, msg)
		}
	}
	if strings.Count(msg, "groq (") != 1 {
		t.Errorf("same provider listed more than once: %q", msg)
	}
}

// TestUpstreamErrorMessageFallback 单条 403 仍带区域封锁提示（既有行为
// 保留在摘要里）；非分发错误走脱敏回退。
func TestUpstreamErrorMessageFallback(t *testing.T) {
	de := &routing.DispatchError{Attempts: []routing.Attempt{{
		Provider: "groq", Model: "llama-x", Kind: routing.KindAuthInvalid,
		Err: &provider.UpstreamError{Provider: "groq", Status: http.StatusForbidden, Body: []byte("Forbidden")},
	}}}
	msg := upstreamErrorMessage(de)
	if !strings.Contains(msg, "HTTP 403 auth_invalid") || !strings.Contains(msg, "region-blocked") {
		t.Errorf("403 message = %q, want status + region hint", msg)
	}
	plain := upstreamErrorMessage(&url.Error{Op: "Post", URL: "https://up.local", Err: errors.New("boom")})
	if plain == "" {
		t.Error("non-dispatch error must still produce a message")
	}
}

// TestDashboardKeysSetClearsIsolation 换 key 清掉该家隔离态：旧 key 留下
// 的 30 分钟鉴权冷却持久化在 SQLite，不清的话"修好 key → 重启"之后仍要
// 空等旧冷却到期。
func TestDashboardKeysSetClearsIsolation(t *testing.T) {
	iso, err := routing.NewIsolation(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	iso.ApplyFailure("groq", "llama-x", routing.KindAuthInvalid)
	gw, s, _ := newDashFixture(t, &routing.Router{Isolation: iso})
	s.SetKeySources(map[string]string{"groq": "none"})
	kr, err := security.Open("")
	if err != nil {
		t.Fatalf("keyring open: %v", err)
	}
	s.SetKeyring(kr)

	if blocked, _ := iso.Block("groq"); !blocked {
		t.Fatal("precondition: groq should be cooled down before the key is replaced")
	}
	body := `{"provider":"groq","key":"sk-fresh-key-xyz"}`
	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/dashboard/api/keys/set?key="+testGatewayToken, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := gw.Client().Do(req)
	if err != nil {
		t.Fatalf("POST keys/set: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("keys/set = %d, want 200", resp.StatusCode)
	}
	if blocked, reason := iso.Block("groq"); blocked {
		t.Fatalf("groq still isolated after its key was replaced: %q", reason)
	}
	if locked, _ := iso.LockedModel("groq", "llama-x"); locked {
		t.Fatal("model lockout from the old key must be cleared too")
	}
}
