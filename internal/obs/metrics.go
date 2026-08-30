// metrics.go 是 Prometheus 指标面：数据面请求计数/时延直方图、
// 流式 TTFT、token 用量、guardrails 发现与逐 attempt 上游失败。指标族
// 面向 Grafana 常规看板（rate/histogram_quantile 直接可用）；标签基数
// 受控（endpoint×provider×status/kind，无自由文本）。/metrics 经
// promhttp 暴露自包含 registry（含 go/process 默认收集器），不污染
// 全局 DefaultRegistry——测试可随意多实例。
package obs

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// LLM 时延分桶：首 token 与整请求共用两个尺度——免费上游慢尾明显，
// 上探到 5 分钟（ 流式 300s 上限）。
var (
	durationBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60, 120, 300}
	ttftBuckets     = []float64{0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}
)

// Metrics 承载网关 Prometheus 指标。nil 接收者全方法 no-op——
// server 未装配（metrics.enabled=false）时调用侧零开销。
type Metrics struct {
	reg        *prometheus.Registry
	requests   *prometheus.CounterVec   // {endpoint, provider, status}
	duration   *prometheus.HistogramVec // {endpoint, provider}
	ttft       *prometheus.HistogramVec // {endpoint, provider}
	tokens     *prometheus.CounterVec   // {provider, direction}
	guardrails *prometheus.CounterVec   // {kind, rule, action}
	failures   *prometheus.CounterVec   // {provider, kind}
}

// NewMetrics 构造并注册全部指标族（自包含 registry）。
func NewMetrics() *Metrics {
	m := &Metrics{
		reg: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "omnifusion_requests_total",
			Help: "Data-plane LLM requests by endpoint, winning provider (cache on semantic-cache hit) and HTTP status.",
		}, []string{"endpoint", "provider", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "omnifusion_request_duration_seconds",
			Help:    "Server-side handling time of data-plane requests.",
			Buckets: durationBuckets,
		}, []string{"endpoint", "provider"}),
		ttft: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "omnifusion_ttft_seconds",
			Help:    "Time to first streamed chunk (measured from endpoint entry).",
			Buckets: ttftBuckets,
		}, []string{"endpoint", "provider"}),
		tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "omnifusion_tokens_total",
			Help: "Token usage by provider and direction (from upstream usage reporting; best effort).",
		}, []string{"provider", "direction"}),
		guardrails: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "omnifusion_guardrails_findings_total",
			Help: "Guardrails findings by kind, rule and action (rule names only, never matched content).",
		}, []string{"kind", "rule", "action"}),
		failures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "omnifusion_upstream_failures_total",
			Help: "Failed upstream attempts by provider and normalized error kind.",
		}, []string{"provider", "kind"}),
	}
	m.reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.requests, m.duration, m.ttft, m.tokens, m.guardrails, m.failures,
	)
	return m
}

// RecordRequest 记一次数据面请求结果：计数 + 时延直方图。
// endpoint ∈ {chat, messages, gemini}；provider 是赢家（缓存命中
// 传 "cache"，护栏拦截等未分发场景传 "none"）。
func (m *Metrics) RecordRequest(endpoint, provider string, status int, d time.Duration) {
	if m == nil {
		return
	}
	m.requests.WithLabelValues(endpoint, provider, strconv.Itoa(status)).Inc()
	m.duration.WithLabelValues(endpoint, provider).Observe(d.Seconds())
}

// RecordTTFT 记流式请求首 chunk 时延（endpoint 入口到首事件）。
func (m *Metrics) RecordTTFT(endpoint, provider string, d time.Duration) {
	if m == nil {
		return
	}
	m.ttft.WithLabelValues(endpoint, provider).Observe(d.Seconds())
}

// RecordTokens 记 token 用量（上游 usage 口径，尽力而为）。
func (m *Metrics) RecordTokens(provider string, prompt, completion int) {
	if m == nil {
		return
	}
	if prompt > 0 {
		m.tokens.WithLabelValues(provider, "prompt").Add(float64(prompt))
	}
	if completion > 0 {
		m.tokens.WithLabelValues(provider, "completion").Add(float64(completion))
	}
}

// RecordGuardrail 记一条护栏发现（kind/rule/action 均为受控词表）。
func (m *Metrics) RecordGuardrail(kind, rule, action string) {
	if m == nil {
		return
	}
	m.guardrails.WithLabelValues(kind, rule, action).Inc()
}

// RecordAttemptFailure 记一次失败的上游尝试（全部候选耗尽前的逐家
// 失败都在此，赢家之前的轮空；kind 是 ErrorKind 词表）。
func (m *Metrics) RecordAttemptFailure(provider, kind string) {
	if m == nil {
		return
	}
	m.failures.WithLabelValues(provider, kind).Inc()
}

// Handler 暴露 /metrics（Prometheus 文本协议）。
func (m *Metrics) Handler() http.Handler {
	if m == nil {
		return http.NotFoundHandler()
	}
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
