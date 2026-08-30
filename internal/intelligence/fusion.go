// fusion.go 是 Fusion（「多模型扇出 + 轻量 Judge 合成」，
// 学 FreeLLMAPI QUORUM 门控）：@fusion 请求并行扇出到 N 个异构成员，
// 成功数 ≥ QUORUM（且 ≥2）时由 Judge 模型合成终稿；不足门控但 ≥1
// 成功时降级直通首个成功成员；全失败上抛错误。分发原语经 DispatchFunc
// 注入（server 层走 router.WithTarget，隔离/配额/打分照常生效），
// 本包不依赖 routing（L5→L3 跨层经显式接口，）。
package intelligence

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// DefaultFusionQuorum 是门控缺省值（学 FreeLLMAPI：≥2 家成功才合成）。
const DefaultFusionQuorum = 2

// FusionMember 是扇出/合成目标声明（provider + 发往该上游的模型）。
type FusionMember struct {
	Provider string
	Model    string
}

// FusionRunner 执行 @fusion 请求。零值不可用：Members 空 → Execute
// 直接报错（未配置在 server 边界已拦 400）。
type FusionRunner struct {
	Members []FusionMember // 扇出成员（声明序 = 降级直通优先级）
	Judge   FusionMember   // 合成 Judge；零值 = Members[0]
	Quorum  int            // 门控阈值；<=0 → DefaultFusionQuorum
	Log     *slog.Logger
}

// FusionAttempt 是一个扇出成员的结果（观测面：日志/测试断言）。
type FusionAttempt struct {
	Provider string
	Model    string
	OK       bool
	Err      error
	Text     string           // 成功时的响应文本
	Resp     *schema.Response // 成功时的完整响应（直通降级复用）
	Usage    *schema.Usage    // 成功时的上游用量
}

// FusionResult 是一次 @fusion 执行的产出。
type FusionResult struct {
	Response    *schema.Response // 终稿（Judge 合成或降级直通）
	Synthesized bool             // true=Judge 合成；false=单成员直通降级
	Attempts    []FusionAttempt  // 扇出观测（声明序）
	JudgeErr    error            // Judge 失败但已降级直通时有值
}

// DispatchFunc 定向分发原语：把 req 发往 (provider, model)。
// 返回错误 = 该成员失败（含隔离短路/上游 4xx5xx/翻译失败）。
type DispatchFunc func(ctx context.Context, provider, model string, req *schema.UnifiedRequest) (*schema.Response, error)

// judgeInstruction 是合成指令（轻量 Judge：只做多数信号合并——多候选
// 共同支持的内容优先、互补细节合并、孤证可疑内容丢弃、跟随请求语言）。
const judgeInstruction = "The request above was answered in parallel by several independent models. " +
	"Synthesize the single best final answer from their candidate answers below: " +
	"prefer statements supported by multiple candidates, merge complementary details, " +
	"and discard anything that appears in only one candidate and looks wrong. " +
	"Answer in the language of the request. Reply with the final answer only."

// Execute 执行扇出 → 门控 → 合成。req 不被修改（逐成员改写模型名的
// 责任在 DispatchFunc 实现侧）。
func (f *FusionRunner) Execute(ctx context.Context, req *schema.UnifiedRequest, dispatch DispatchFunc) (*FusionResult, error) {
	if len(f.Members) == 0 {
		return nil, errors.New("fusion: no members configured")
	}
	if dispatch == nil {
		return nil, errors.New("fusion: no dispatch function")
	}
	atts := f.fanout(ctx, req, dispatch)

	ok := successes(atts)
	if len(ok) == 0 {
		return nil, fmt.Errorf("fusion: all %d member(s) failed", len(atts))
	}
	if len(ok) < 2 || len(ok) < f.quorum() { // 门控未过：单成员降级直通
		return &FusionResult{Response: atts[ok[0]].Resp, Attempts: atts}, nil
	}
	return f.synthesize(ctx, req, atts, ok, dispatch)
}

// synthesize 由 Judge 合成终稿；Judge 失败降级直通首个成功成员。
func (f *FusionRunner) synthesize(ctx context.Context, req *schema.UnifiedRequest, atts []FusionAttempt, ok []int, dispatch DispatchFunc) (*FusionResult, error) {
	judge := f.Judge
	if judge.Provider == "" || judge.Model == "" {
		judge = FusionMember{Provider: f.Members[0].Provider, Model: f.Members[0].Model}
	}
	resp, err := dispatch(ctx, judge.Provider, judge.Model, judgeRequest(req, atts, ok))
	if err != nil {
		if f.Log != nil {
			f.Log.Warn("fusion judge failed; degrading to first success",
				"provider", judge.Provider, "model", judge.Model, "err", err)
		}
		return &FusionResult{Response: atts[ok[0]].Resp, Attempts: atts, JudgeErr: err}, nil
	}
	sumUsage(resp, atts, ok)
	return &FusionResult{Response: resp, Synthesized: true, Attempts: atts}, nil
}

// fanout 并行扇出全部成员；返回声明序结果（逐槽写入，无锁）。
func (f *FusionRunner) fanout(ctx context.Context, req *schema.UnifiedRequest, dispatch DispatchFunc) []FusionAttempt {
	atts := make([]FusionAttempt, len(f.Members))
	var wg sync.WaitGroup
	for i, m := range f.Members {
		wg.Add(1)
		go func(i int, m FusionMember) {
			defer wg.Done()
			resp, err := dispatch(ctx, m.Provider, m.Model, req)
			a := FusionAttempt{Provider: m.Provider, Model: m.Model, Err: err}
			if err == nil && resp != nil {
				a.OK, a.Text, a.Resp, a.Usage = true, responseText(resp), resp, resp.Usage
			}
			atts[i] = a
		}(i, m)
	}
	wg.Wait()
	return atts
}

// successes 返回成功成员下标（声明序）。
func successes(atts []FusionAttempt) []int {
	var ok []int
	for i, a := range atts {
		if a.OK {
			ok = append(ok, i)
		}
	}
	return ok
}

// quorum 解析门控阈值（配置校验兜底：钳到 [2, members]）。
func (f *FusionRunner) quorum() int {
	q := f.Quorum
	if q <= 0 {
		q = DefaultFusionQuorum
	}
	if q < 2 {
		q = 2
	}
	if q > len(f.Members) {
		q = len(f.Members)
	}
	return q
}

// judgeRequest 构造 Judge 合成请求：保留原会话（多轮上下文），追加
// 候选答案块；关流式/工具（合成只产终稿文本），ResponseFormat 保留
// （结构化输出的格式合规由 Judge 兜住）。
func judgeRequest(req *schema.UnifiedRequest, atts []FusionAttempt, ok []int) *schema.UnifiedRequest {
	var b strings.Builder
	b.WriteString(judgeInstruction)
	for n, i := range ok {
		_, _ = fmt.Fprintf(&b, "\n\nCandidate %d (%s/%s):\n%s", n+1, atts[i].Provider, atts[i].Model, atts[i].Text)
	}
	jr := *req // 浅拷贝：Messages 换新切片，其余字段按值共享
	jr.Stream = false
	jr.Tools = nil
	jr.ToolChoice = nil
	jr.Messages = append(append([]schema.Message(nil), req.Messages...),
		schema.Message{Role: schema.RoleUser, Content: schema.NewTextContent(b.String())})
	return &jr
}

// sumUsage 把扇出成员 + Judge 的用量并入终稿 Usage（真实成本口径）。
func sumUsage(resp *schema.Response, atts []FusionAttempt, ok []int) {
	if resp == nil {
		return
	}
	u := &schema.Usage{}
	if resp.Usage != nil {
		*u = *resp.Usage
	}
	for _, i := range ok {
		if atts[i].Usage != nil {
			u.PromptTokens += atts[i].Usage.PromptTokens
			u.CompletionTokens += atts[i].Usage.CompletionTokens
			u.TotalTokens += atts[i].Usage.TotalTokens
		}
	}
	resp.Usage = u
}

// responseText 提取响应文本（choices[].message.content 的文本部分）。
func responseText(resp *schema.Response) string {
	if resp == nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range resp.Choices {
		for _, p := range c.Message.Content.Parts {
			if p.Type == schema.PartText {
				sb.WriteString(p.Text)
			}
		}
	}
	return sb.String()
}
