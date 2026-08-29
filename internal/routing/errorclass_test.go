package routing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/provider"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorKind
	}{
		// —— rate_limit vs quota_exhausted ——
		{"429 plain", &provider.UpstreamError{Status: 429, Body: []byte(`{"error":{"message":"Rate limit reached. Try again later"}}`)}, KindRateLimit},
		{"429 wrapped", fmt.Errorf("upstream: %w", &provider.UpstreamError{Status: 429, Body: []byte(`too many requests`)}), KindRateLimit},
		{"429 insufficient_quota", &provider.UpstreamError{Status: 429, Body: []byte(`You exceeded your current quota, please check your plan and billing details`)}, KindQuotaExhausted},
		{"429 free-models-per-day credits", &provider.UpstreamError{Status: 429, Body: []byte(`free-models-per-day reached, please add credits`)}, KindQuotaExhausted},
		{"402 payment required", &provider.UpstreamError{Status: 402, Body: []byte(`{"error":{"message":"This key is out of credits"}}`)}, KindQuotaExhausted},
		// —— auth_invalid ——
		{"401", &provider.UpstreamError{Status: 401, Body: []byte(`invalid api key`)}, KindAuthInvalid},
		{"403 region block", &provider.UpstreamError{Status: 403, Body: []byte(`{"error":{"message":"Request not allowed from your region"}}`)}, KindAuthInvalid},
		// —— upstream_5xx ——
		{"500", &provider.UpstreamError{Status: 500, Body: []byte(`internal error`)}, KindUpstream5xx},
		{"502", &provider.UpstreamError{Status: 502}, KindUpstream5xx},
		{"503 overloaded", &provider.UpstreamError{Status: 503, Body: []byte(`overloaded`)}, KindUpstream5xx},
		{"529 cloudflare", &provider.UpstreamError{Status: 529}, KindUpstream5xx},
		// —— net_or_timeout ——
		{"deadline exceeded", context.DeadlineExceeded, KindNetOrTimeout},
		{"deadline wrapped", fmt.Errorf("call: %w", context.DeadlineExceeded), KindNetOrTimeout},
		{"client canceled", context.Canceled, KindNetOrTimeout},
		{"dns timeout", &net.DNSError{Err: "connection timed out", IsTimeout: true}, KindNetOrTimeout},
		{"url error conn refused", &url.Error{Op: "Post", URL: "https://up.local/v1", Err: errors.New("dial tcp: connect: connection refused")}, KindNetOrTimeout},
		// —— stream_broken ——
		{"stream read", &provider.StreamError{Provider: "p", Reason: provider.StreamRead, Err: io.ErrUnexpectedEOF}, KindStreamBroken},
		{"stream ended without done", &provider.StreamError{Provider: "p", Reason: provider.StreamEndedWithoutDone, Err: io.ErrUnexpectedEOF}, KindStreamBroken},
		{"stream decode", &provider.StreamError{Provider: "p", Reason: provider.StreamDecode, Err: errors.New("bad json")}, KindStreamBroken},
		{"stream wrapped", fmt.Errorf("stream first chunk: %w", &provider.StreamError{Provider: "p", Reason: provider.StreamRead, Err: errors.New("reset")}), KindStreamBroken},
		{"inline error event (status 0)", &provider.UpstreamError{Status: 0, Body: []byte(`{"error":{"message":"overloaded"}}`)}, KindStreamBroken},
		// —— request_error / unknown / nil ——
		{"400 bad request", &provider.UpstreamError{Status: 400, Body: []byte(`invalid parameter`)}, KindRequestError},
		{"404 model not found", &provider.UpstreamError{Status: 404, Body: []byte(`model nope does not exist`)}, KindRequestError},
		{"413 too large", &provider.UpstreamError{Status: 413}, KindRequestError},
		{"plain error", errors.New("boom"), KindUnknown},
		{"nil", nil, ""},
	}
	for _, tc := range cases {
		if got := Classify(tc.err); got != tc.want {
			t.Errorf("%s: Classify = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPolicyFor(t *testing.T) {
	cases := []struct {
		kind ErrorKind
		want Policy
	}{
		{KindRateLimit, Policy{Failover: true, Cooldown: 30 * time.Second}},
		{KindQuotaExhausted, Policy{Failover: true, LockoutModel: true}},
		{KindAuthInvalid, Policy{Failover: true, Cooldown: 30 * time.Minute, InvalidKey: true}},
		{KindUpstream5xx, Policy{Failover: true, HealthPenalty: true}},
		{KindNetOrTimeout, Policy{Failover: true}},
		{KindStreamBroken, Policy{Failover: true, HealthPenalty: true}},
		{KindRequestError, Policy{Failover: true}},
		{KindUnknown, Policy{Failover: true}},
		{"", Policy{Failover: true}},
	}
	for _, tc := range cases {
		if got := PolicyFor(tc.kind); got != tc.want {
			t.Errorf("PolicyFor(%q) = %+v, want %+v", tc.kind, got, tc.want)
		}
	}
}

func TestErrorKindLabel(t *testing.T) {
	if got := KindRateLimit.Label("msg"); got != "rate_limit: msg" {
		t.Errorf("Label = %q", got)
	}
	if got := ErrorKind("").Label("msg"); got != "msg" {
		t.Errorf("empty Label = %q", got)
	}
}

// TestClassifyDistinguishesRateLimitFromQuota 钉住 429 双语义：同样 429，
// body 关键词决定冷却连接还是锁定模型。
func TestClassifyDistinguishesRateLimitFromQuota(t *testing.T) {
	rl := &provider.UpstreamError{Status: 429, Body: []byte(`Rate limit reached for requests`)}
	qe := &provider.UpstreamError{Status: 429, Body: []byte(`You exceeded your APPLICATION quota`)}

	if k := Classify(rl); k != KindRateLimit || PolicyFor(k).Cooldown == 0 || PolicyFor(k).LockoutModel {
		t.Errorf("plain 429 policy mismatch: kind=%q policy=%+v", k, PolicyFor(k))
	}
	if k := Classify(qe); k != KindQuotaExhausted || PolicyFor(k).LockoutModel != true || PolicyFor(k).Cooldown != 0 {
		t.Errorf("quota 429 policy mismatch: kind=%q policy=%+v", k, PolicyFor(k))
	}
}
