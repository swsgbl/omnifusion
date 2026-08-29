package intelligence

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// fakeDispatch 记录逐调用 (provider, model, len(messages)) 并按
// provider/model 键控失败；成功返回带用量与文本的响应。
type fakeDispatch struct {
	mu    sync.Mutex
	calls []fakeCall
	fail  map[string]bool
}

type fakeCall struct {
	provider string
	model    string
	msgs     int
}

func (f *fakeDispatch) call(ctx context.Context, provider, model string, req *schema.UnifiedRequest) (*schema.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeCall{provider: provider, model: model, msgs: len(req.Messages)})
	if f.fail[provider+"/"+model] {
		return nil, errors.New("boom: " + provider)
	}
	return memberResp(provider, model), nil
}

func (f *fakeDispatch) snapshot() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall(nil), f.calls...)
}

func memberResp(provider, model string) *schema.Response {
	return &schema.Response{
		Model:        model,
		ProviderName: provider,
		Choices:      []schema.ResponseChoice{{Message: schema.Message{Role: schema.RoleAssistant, Content: schema.NewTextContent("answer from " + provider)}}},
		Usage:        &schema.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
}

func fusionReq() *schema.UnifiedRequest {
	return &schema.UnifiedRequest{
		Model:    "@fusion",
		Messages: []schema.Message{{Role: schema.RoleUser, Content: schema.NewTextContent("what is 2+2?")}},
	}
}

func threeMembers() []FusionMember {
	return []FusionMember{
		{Provider: "a", Model: "model-a"},
		{Provider: "b", Model: "model-b"},
		{Provider: "c", Model: "model-c"},
	}
}

func TestFusionSynthesizesWithJudge(t *testing.T) {
	fd := &fakeDispatch{}
	f := &FusionRunner{Members: threeMembers()} // Judge 缺省 = a/model-a
	res, err := f.Execute(context.Background(), fusionReq(), fd.call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Synthesized {
		t.Fatal("Synthesized = false, want true (3 成员全过门控)")
	}
	if res.JudgeErr != nil {
		t.Fatalf("JudgeErr = %v, want nil", res.JudgeErr)
	}
	calls := fd.snapshot()
	if len(calls) != 4 { // 3 扇出 + 1 合成
		t.Fatalf("dispatch calls = %d, want 4: %+v", len(calls), calls)
	}
	// 扇出 3 调用无序（并行）：按 model 排序后断言成员齐全。
	var fanoutModels []string
	for _, c := range calls[:3] {
		if c.msgs != 1 {
			t.Errorf("扇出调用 messages=%d, want 1（不追加候选）", c.msgs)
		}
		fanoutModels = append(fanoutModels, c.model)
	}
	sort.Strings(fanoutModels)
	want := []string{"model-a", "model-b", "model-c"}
	for i := range want {
		if fanoutModels[i] != want[i] {
			t.Errorf("fanout models = %v, want %v", fanoutModels, want)
		}
	}
	judge := calls[3]
	if judge.provider != "a" || judge.model != "model-a" {
		t.Errorf("judge = %s/%s, want a/model-a（缺省首成员）", judge.provider, judge.model)
	}
	if judge.msgs != 2 { // 原会话 1 条 + 候选块 1 条
		t.Fatalf("judge messages = %d, want 2", judge.msgs)
	}
	// 用量汇总口径：扇出成功 3 家 + judge 1 次 = 4×(10/5/15)。
	u := res.Response.Usage
	if u.PromptTokens != 40 || u.CompletionTokens != 20 || u.TotalTokens != 60 {
		t.Errorf("usage = %d/%d/%d, want 40/20/60", u.PromptTokens, u.CompletionTokens, u.TotalTokens)
	}
}

// judgeRequest 内容断言（经 white-box：候选块含指令与各家答案）。
func TestFusionJudgePromptContainsCandidates(t *testing.T) {
	atts := []FusionAttempt{
		{Provider: "a", Model: "model-a", OK: true, Text: "answer from a"},
		{Provider: "b", Model: "model-b", OK: true, Text: "answer from b"},
	}
	jr := judgeRequest(fusionReq(), atts, []int{0, 1})
	last := jr.Messages[len(jr.Messages)-1]
	var sb strings.Builder
	for _, p := range last.Content.Parts {
		sb.WriteString(p.Text)
	}
	prompt := sb.String()
	for _, want := range []string{"Candidate 1 (a/model-a)", "answer from a", "Candidate 2 (b/model-b)", "answer from b"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("judge prompt 缺 %q", want)
		}
	}
	if len(jr.Messages) != 2 || jr.Messages[0].Role != schema.RoleUser {
		t.Errorf("judge messages 形状不符：%d 条", len(jr.Messages))
	}
	if jr.Stream || jr.Tools != nil || jr.ToolChoice != nil {
		t.Error("judge 请求应关流式/工具")
	}
}

func TestFusionQuorumNotMetDegradesToSingle(t *testing.T) {
	fd := &fakeDispatch{fail: map[string]bool{"a/model-a": true, "c/model-c": true}}
	f := &FusionRunner{Members: threeMembers()} // quorum=2，仅 b 成功
	res, err := f.Execute(context.Background(), fusionReq(), fd.call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Synthesized {
		t.Fatal("Synthesized = true, want false（门控未过降级）")
	}
	if res.Response == nil || res.Response.ProviderName != "b" {
		t.Fatalf("降级响应 provider = %v, want b", res.Response)
	}
	if len(fd.snapshot()) != 3 { // 只有扇出，无 judge
		t.Errorf("dispatch calls = %d, want 3", len(fd.snapshot()))
	}
}

func TestFusionAllFailReturnsError(t *testing.T) {
	fd := &fakeDispatch{fail: map[string]bool{
		"a/model-a": true, "b/model-b": true, "c/model-c": true,
	}}
	f := &FusionRunner{Members: threeMembers()}
	if _, err := f.Execute(context.Background(), fusionReq(), fd.call); err == nil {
		t.Fatal("err = nil, want 全失败上抛")
	}
}

func TestFusionJudgeFailureFallsBack(t *testing.T) {
	// 门控过（b/c 成功，a 扇出失败）；judge 显式指向 a → 也失败 → 降级 b。
	fd := &fakeDispatch{fail: map[string]bool{"a/model-a": true}}
	f := &FusionRunner{
		Members: threeMembers(),
		Judge:   FusionMember{Provider: "a", Model: "model-a"},
	}
	res, err := f.Execute(context.Background(), fusionReq(), fd.call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Synthesized {
		t.Fatal("Synthesized = true, want false（judge 失败降级）")
	}
	if res.JudgeErr == nil {
		t.Fatal("JudgeErr = nil, want 记录失败原因")
	}
	if res.Response == nil || res.Response.ProviderName != "b" {
		t.Fatalf("降级响应 provider = %v, want b（首个成功成员）", res.Response)
	}
}

func TestFusionQuorumThreeRequiresThree(t *testing.T) {
	fd := &fakeDispatch{fail: map[string]bool{"c/model-c": true}}
	f := &FusionRunner{Members: threeMembers(), Quorum: 3} // 2 成功 < 3
	res, err := f.Execute(context.Background(), fusionReq(), fd.call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Synthesized {
		t.Fatal("quorum=3 且仅 2 成功时不应合成")
	}
	if res.Response.ProviderName != "a" {
		t.Errorf("降级 provider = %s, want a", res.Response.ProviderName)
	}
}

func TestFusionExecuteGuards(t *testing.T) {
	f := &FusionRunner{}
	if _, err := f.Execute(context.Background(), fusionReq(), func(context.Context, string, string, *schema.UnifiedRequest) (*schema.Response, error) {
		return nil, nil
	}); err == nil {
		t.Fatal("空成员应报错")
	}
	f.Members = threeMembers()
	if _, err := f.Execute(context.Background(), fusionReq(), nil); err == nil {
		t.Fatal("空 dispatch 应报错")
	}
}
