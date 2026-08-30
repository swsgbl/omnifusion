// semantic_sidecar.go 是语义压缩可选档（ 6.2，）：LLMLingua-2
// 级神经压缩经 sidecar HTTP 服务（学习型模型一律进程外部署），
// 默认二进制零模型依赖红线不动。装配期 ConfigureSemantic 注入 sidecar
// URL 与保留率；URL 未配置时本阶段 ShouldRun 恒 false（纯规则档工作）；
// sidecar 网络/协议失败时 Apply 报错——管线回退原文直传（
// 规则 3），压缩失败永不阻断请求。
package compression

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// sidecarTimeout 是单次 sidecar 调用的硬上限：压缩是增益路径，
// 超时即放弃（原文直传），不拖累请求时延。
const sidecarTimeout = 3 * time.Second

// semanticSettings 是装配期注入的语义压缩运行参数（cmd/ofd 一次
// 写入，阶段实例在 BuildCombo 构造、运行时读取）。
var semanticSettings struct {
	sync.Mutex
	rate float64
	url  string
}

// ConfigureSemantic 注入语义压缩参数：rate 钳 [0.1,0.9]（0 取默认
// 0.5）；sidecarURL 空 = sidecar 档不可用（仅规则档）。
func ConfigureSemantic(rate float64, sidecarURL string) {
	semanticSettings.Lock()
	defer semanticSettings.Unlock()
	semanticSettings.rate = clampRate(rate)
	semanticSettings.url = sidecarURL
}

// clampRate 把保留率钳进 [0.1,0.9]（0/越界取默认 0.5）——过低保真
// 风险、过高无意义，配置错误不静默放大。
func clampRate(r float64) float64 {
	if r <= 0 || r > 1 {
		return defaultSemanticKeepRate
	}
	if r < 0.1 {
		return 0.1
	}
	if r > 0.9 {
		return 0.9
	}
	return r
}

func configuredSemanticRate() float64 {
	semanticSettings.Lock()
	defer semanticSettings.Unlock()
	if semanticSettings.rate <= 0 {
		return defaultSemanticKeepRate
	}
	return semanticSettings.rate
}

// sidecarRequest / sidecarResponse 是 sidecar 的窄协议：文本数组
// 进出、对位替换——服务端实现语言/模型自由（Python + ONNX 部署
// LLMLingua-2），网关侧只约定 JSON 形态。
type sidecarRequest struct {
	Texts []string `json:"texts"`
	Rate  float64  `json:"rate"`
}

type sidecarResponse struct {
	Texts []string `json:"texts"`
}

// SidecarStage 实现 CompressionStage：可选神经压缩档。
type SidecarStage struct {
	client *http.Client

	mu     sync.Mutex
	totals SemanticTotals
}

// NewSidecarStage 构造阶段（参数运行时读包级配置）。
func NewSidecarStage() *SidecarStage {
	return &SidecarStage{client: &http.Client{Timeout: sidecarTimeout}}
}

// Name 实现 CompressionStage。
func (s *SidecarStage) Name() string { return "semantic_sidecar" }

// ShouldRun 实现 CompressionStage：sidecar 未配置恒跳过。
func (s *SidecarStage) ShouldRun(sc *StageContext) bool {
	semanticSettings.Lock()
	url := semanticSettings.url
	semanticSettings.Unlock()
	return url != "" && sc.EstimatedTokens >= defaultSemanticMinTokens
}

// Apply 实现 CompressionStage：收集合格长文本批量送 sidecar，对位
// 替换。sidecar 出错即本阶段失败（原文直传），不部分采纳。
func (s *SidecarStage) Apply(msgs []schema.Message) ([]schema.Message, CompressionStats, error) {
	var stats CompressionStats
	semanticSettings.Lock()
	rate, url := semanticSettings.rate, semanticSettings.url
	semanticSettings.Unlock()
	if url == "" {
		return nil, stats, fmt.Errorf("semantic sidecar not configured")
	}
	before := EstimateTokens(msgs)
	out := make([]schema.Message, len(msgs))
	copy(out, msgs)
	guardStart := len(msgs) - min(defaultRecencyWindow, len(msgs))
	var texts []string
	var at []int
	for i, m := range msgs {
		text := singleTextOf(m)
		if i >= guardStart || !cavemanEligible(m) ||
			len(text) < defaultSemanticMinTextChars {
			continue
		}
		texts = append(texts, text)
		at = append(at, i)
	}
	stats = CompressionStats{Stage: s.Name(), Applied: true, BeforeTokens: before}
	if len(texts) == 0 {
		stats.AfterTokens = before
		return out, stats, nil
	}
	compressed, err := s.callSidecar(url, rate, texts)
	if err != nil {
		return nil, stats, err
	}
	rewritten, charsBefore, charsSaved := 0, 0, 0
	for j, i := range at {
		if compressed[j] == texts[j] {
			continue
		}
		rewritten++
		charsBefore += len(texts[j])
		charsSaved += len(texts[j]) - len(compressed[j])
		out[i].Content = schema.NewTextContent(compressed[j])
	}
	stats.AfterTokens = EstimateTokens(out)
	stats.Saved = before - stats.AfterTokens
	s.record(rewritten, charsBefore, charsSaved)
	return out, stats, nil
}

// callSidecar POST {texts,rate} → {texts}；非 200 / 数量不符 / 超时
// 都是错误（调用方决定回退）。
func (s *SidecarStage) callSidecar(url string, rate float64, texts []string) ([]string, error) {
	body, err := json.Marshal(sidecarRequest{Texts: texts, Rate: rate})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), sidecarTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("semantic sidecar: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("semantic sidecar: status %d", resp.StatusCode)
	}
	var out sidecarResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("semantic sidecar: decode: %w", err)
	}
	if len(out.Texts) != len(texts) {
		return nil, fmt.Errorf("semantic sidecar: got %d texts, want %d", len(out.Texts), len(texts))
	}
	return out.Texts, nil
}

// Totals 返回跨请求累计计数快照（观测用）。
func (s *SidecarStage) Totals() SemanticTotals {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totals
}

func (s *SidecarStage) record(rewritten, before, saved int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totals.Runs++
	s.totals.MessagesRewritten += int64(rewritten)
	s.totals.CharsBefore += int64(before)
	s.totals.CharsSaved += int64(saved)
}
