// probe.go 是模型可用性探测（dsh probe.js 借鉴，v0.1.32）：providers
// 页展开行的「探测」按钮对每模型发一条极小真实请求（max_tokens 16，
// "ping"），按保守纪律判五态——只有"网关明确点名该模型不可路由"
//（404/400/422 或 unavailable/not supported/no such model 等消息，
// 且非 5xx）才判不可用；429=限流；5xx/401/403/超时/传输=未知
//（不定罪：过期条目顶多多点一次失败，误杀要等探测轮）。并发 3
//（配额友好）、单探测 20s、整轮 90s 预算（超时未探测的记 unknown）。
// 探测花真金白银上的极小额度（~20 tokens/模型），按钮文案明示。
package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
)

// 探测五态（dsh 口径）。
const (
	ProbeAvailable   = "available"
	ProbeUnavailable = "unavailable"
	ProbeThrottled   = "throttled"
	ProbeUnknown     = "unknown"
)

const (
	probeMaxTokens     = 16
	probeConcurrency   = 3
	probePerTimeout    = 20 * time.Second
	probeOverallBudget = 90 * time.Second
	probeMaxModels     = 40 // 单轮上限（kilo 带 key 全量 390 家会被目录过滤，仍设帽防锤爆）
)

// probeResult 是一个模型的探测结论。
type probeResult struct {
	Model     string  `json:"model"`
	State     string  `json:"state"`
	LatencyMS float64 `json:"latency_ms"`
	Detail    string  `json:"detail,omitempty"`
	ProbedAt  string  `json:"probed_at"`
}

// probeStore 缓存最近一轮结论（内存态：结论是时点性的，重启即弃）。
type probeStore struct {
	mu sync.Mutex
	m  map[string]map[string]probeResult // provider → model → result
}

var probeResults = &probeStore{m: map[string]map[string]probeResult{}}

func (ps *probeStore) set(providerName string, results []probeResult) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	m := make(map[string]probeResult, len(results))
	for _, r := range results {
		m[r.Model] = r
	}
	ps.m[providerName] = m
}

func (ps *probeStore) get(providerName string) map[string]probeResult {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	out := make(map[string]probeResult, len(ps.m[providerName]))
	for k, v := range ps.m[providerName] {
		out[k] = v
	}
	return out
}

// unroutableMsg 匹配"网关点名模型不可路由"的措辞（非 5xx 时才可信：
// 反代的 503 "Service Unavailable" 不是模型判词）。
var unroutableMsg = regexp.MustCompile(
	`unavailable|not supported|no such model|unknown model|invalid model|does not exist|not found`)

// probeVerdictOf 把一次探测的结局折算成五态（保守纪律的核心）。
// status=0 表示传输层失败（err 有值）。
func probeVerdictOf(status int, body string, err error) (state, detail string) {
	if err != nil {
		return ProbeUnknown, firstLine(err.Error())
	}
	if status >= 200 && status < 300 {
		return ProbeAvailable, ""
	}
	lower := strings.ToLower(body)
	switch {
	case status == 429:
		return ProbeThrottled, firstLine(body)
	case status == 400 || status == 404 || status == 422:
		if unroutableMsg.MatchString(lower) {
			return ProbeUnavailable, firstLine(body)
		}
		return ProbeUnknown, firstLine(body)
	case status >= 500:
		return ProbeUnknown, firstLine(body) // 网关自扰不定罪
	default: // 401/403/407/408/425/其余 4xx：与模型无关
		return ProbeUnknown, firstLine(body)
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// probeProviderModel 对一个 (provider, model) 发极小真实请求。
func probeProviderModel(ctx context.Context, p provider.Provider, model string) probeResult {
	start := time.Now()
	req := &schema.UnifiedRequest{
		Model:    model,
		Messages: []schema.Message{{Role: schema.RoleUser, Content: schema.NewTextContent("ping")}},
	}
	mt := probeMaxTokens
	req.MaxTokens = &mt

	res := probeResult{Model: model, ProbedAt: time.Now().UTC().Format(time.RFC3339)}
	state, detail := ProbeUnknown, ""
	func() {
		defer func() {
			// 防御：探测路径的 panic 一律折算为 unknown，不掀翻端点。
			if r := recover(); r != nil {
				state, detail = ProbeUnknown, "probe panic"
			}
		}()
		call, err := p.Translate(ctx, req)
		if err != nil {
			state, detail = ProbeUnknown, firstLine(err.Error())
			return
		}
		client := p.HTTPClient()
		if client == nil {
			client = http.DefaultClient
		}
		httpReq, err := http.NewRequestWithContext(ctx, call.Method, call.URL, bytes.NewReader(call.Body))
		if err != nil {
			state, detail = ProbeUnknown, firstLine(err.Error())
			return
		}
		httpReq.Header = call.Header
		resp, err := client.Do(httpReq)
		if err != nil {
			state, detail = ProbeUnknown, firstLine(err.Error())
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			// 2xx 空候选=上游伪成功（Parse 的既有判定），探测面按
			// unknown 处理（不定罪：可能是瞬时故障）。
			if strings.Contains(string(body), `"choices"`) &&
				!strings.Contains(string(body), `"choices":[]`) {
				state = ProbeAvailable
				return
			}
			if parsed, perr := p.Parse(ctx, call, respFresh(resp, body)); perr == nil && len(parsed.Choices) > 0 {
				state = ProbeAvailable
				return
			}
			state, detail = ProbeUnknown, "2xx with no usable choices"
			return
		}
		state, detail = probeVerdictOf(resp.StatusCode, string(body), nil)
	}()
	res.State, res.Detail = state, detail
	res.LatencyMS = float64(time.Since(start).Milliseconds())
	return res
}

// probeProvider 对 provider 的（帽内）模型清单做有界并发探测。
// models 由调用方从目录快照取（已在册模型）。
func probeProvider(ctx context.Context, p provider.Provider, models []string) []probeResult {
	if len(models) > probeMaxModels {
		models = models[:probeMaxModels]
	}
	overall, cancel := context.WithTimeout(ctx, probeOverallBudget)
	defer cancel()

	var mu sync.Mutex
	results := make([]probeResult, 0, len(models))
	next := 0
	var wg sync.WaitGroup
	workers := probeConcurrency
	if workers > len(models) {
		workers = len(models)
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				i := next
				next++
				mu.Unlock()
				if i >= len(models) {
					return
				}
				if overall.Err() != nil {
					mu.Lock()
					results = append(results, probeResult{Model: models[i],
						State: ProbeUnknown, Detail: "not probed (deadline)",
						ProbedAt: time.Now().UTC().Format(time.RFC3339)})
					mu.Unlock()
					continue
				}
				pctx, pcancel := context.WithTimeout(overall, probePerTimeout)
				r := probeProviderModel(pctx, p, models[i])
				pcancel()
				mu.Lock()
				results = append(results, r)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	// 按输入序稳定输出（并发完成序不定）。
	byModel := make(map[string]probeResult, len(results))
	for _, r := range results {
		byModel[r.Model] = r
	}
	ordered := make([]probeResult, 0, len(models))
	for _, m := range models {
		if r, ok := byModel[m]; ok {
			ordered = append(ordered, r)
		}
	}
	return ordered
}

// respFresh 用已读 body 重建可重复读的 response（Parse 会再读体）。
func respFresh(resp *http.Response, body []byte) *http.Response {
	clone := *resp
	clone.Body = io.NopCloser(bytes.NewReader(body))
	return &clone
}
