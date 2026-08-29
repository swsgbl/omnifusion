package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/compression"
	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
)

// routeStubWindows 是 WindowResolver 测试桩：key = "provider/model"。
type routeStubWindows map[string]int64

func (s routeStubWindows) ContextWindow(providerName, model string) (int64, bool) {
	w, ok := s[providerName+"/"+model]
	return w, ok
}

// TestCompressionWidensRouterCandidates 是 docs/05 4.5 的验收：
// L4 压缩管线的产出（压缩后 token）经 WithPromptTokens 喂给 L3 路由，
// 小上下文模型因压缩进入候选——压缩前装不下被滤除、压缩后成为首选。
func TestCompressionWidensRouterCandidates(t *testing.T) {
	// 长会话：40 轮重复粘贴的冗长请求（dedup 折叠 + caveman 词面压缩）。
	bloat := strings.Repeat(
		"In order to please proceed, please basically review the attached document very carefully. ", 6)
	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: schema.NewTextContent("You are helpful.")},
	}
	for i := 0; i < 40; i++ {
		msgs = append(msgs, schema.Message{
			Role: schema.RoleUser, Content: schema.NewTextContent(bloat)})
	}
	msgs = append(msgs,
		schema.Message{Role: schema.RoleAssistant, Content: schema.NewTextContent("Working on it.")},
		schema.Message{Role: schema.RoleUser, Content: schema.NewTextContent("Summarize.")})

	before := compression.EstimateTokens(msgs)
	out, stats := compression.NewPipeline(nil,
		compression.NewDedupStage(compression.DedupConfig{MinTokens: 1, RecencyGuard: 2}),
		compression.NewCavemanStage(compression.CavemanConfig{
			MinTokens: 1, MinTextChars: 100, RecencyGuard: 2}),
	).Run(compression.NewStageContext("m", "sess", msgs), msgs)
	after := compression.EstimateTokens(out)
	for i, s := range stats {
		if s.GateRejected != nil || s.Err != nil {
			t.Fatalf("stage %d did not pass: %+v", i, s)
		}
	}

	const smallWindow = 4096
	if before <= smallWindow {
		t.Fatalf("fixture too small: before=%d must exceed %d", before, smallWindow)
	}
	if after >= smallWindow {
		t.Fatalf("compression too weak: after=%d must fit %d", after, smallWindow)
	}

	// 两家候选：a 注册序在前但窗口小（4k，免费层典型），b 窗口大。
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"x","object":"chat.completion","created":1,"model":"m",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer up.Close()
	var provs []provider.Provider
	for _, name := range []string{"a", "b"} {
		p, err := openai_compat.New(openai_compat.Spec{
			ProviderName: name, BaseURL: up.URL + "/v1", APIKey: "k",
		})
		if err != nil {
			t.Fatalf("New(%s): %v", name, err)
		}
		provs = append(provs, p)
	}
	r := &routing.Router{Providers: provs,
		Windows: routeStubWindows{"a/m": smallWindow, "b/m": 200000}}
	req := &schema.UnifiedRequest{Model: "m", Messages: out}

	// 压缩前 token：a 装不下被滤除，首选 b。
	_, attempts, err := r.Dispatch(context.Background(), req, routing.WithPromptTokens(int64(before)))
	if err != nil {
		t.Fatalf("dispatch (before compression): %v", err)
	}
	if attempts[0].Provider != "b" {
		t.Fatalf("pre-compression first attempt = %s, want b (a over window)", attempts[0].Provider)
	}

	// 压缩后 token：a 进入候选并按注册序成为首选。
	_, attempts2, err := r.Dispatch(context.Background(), req, routing.WithPromptTokens(int64(after)))
	if err != nil {
		t.Fatalf("dispatch (after compression): %v", err)
	}
	if attempts2[0].Provider != "a" {
		t.Fatalf("post-compression first attempt = %s, want a (small window fits)", attempts2[0].Provider)
	}
}
