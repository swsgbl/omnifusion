package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/catalogfeed"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
)

// newQualityFixture 装配 @quality 测试网关：两个 mock 上游，weak 家
// 注册序在前（若无能力分它先被尝试）；feed 给 strong 家的模型高分。
// 返回 (网关, Server 引用, 赢家指针)——Server 供测试摘除能力源。
func newQualityFixture(t *testing.T) (*httptest.Server, *Server, *string) {
	t.Helper()
	winner := ""
	newUp := func(name string) *httptest.Server {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var p map[string]any
			_ = json.NewDecoder(r.Body).Decode(&p)
			winner = name
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"`+
				`m","choices":[{"index":0,"message":{"role":"assistant","content":"`+name+`"},`+
				`"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		}))
		t.Cleanup(up.Close)
		return up
	}
	upWeak, upStrong := newUp("weak"), newUp("strong")

	weak, err := openai_compat.New(openai_compat.Spec{ProviderName: "weak", BaseURL: upWeak.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("weak adapter: %v", err)
	}
	strong, err := openai_compat.New(openai_compat.Spec{ProviderName: "strong", BaseURL: upStrong.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("strong adapter: %v", err)
	}

	catalog := routing.NewCatalog(nil, nil, nil, nil, nil)
	catalog.ApplyFeed(&catalogfeed.Feed{
		Version: 1, GeneratedAt: 1,
		Providers: map[string]catalogfeed.ProviderFeed{
			"weak":   {Models: []catalogfeed.ModelEntry{{ID: "m", Status: "stable", Capability: 40}}},
			"strong": {Models: []catalogfeed.ModelEntry{{ID: "m", Status: "stable", Capability: 95}}},
		},
	})

	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	router := &routing.Router{Providers: []provider.Provider{weak, strong}} // weak 注册序在前
	router.Capability = catalog
	s.SetRouter(router)
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)
	return gw, s, &winner
}

func TestQualityDirectivePicksStrongest(t *testing.T) {
	gw, _, winner := newQualityFixture(t)
	resp := postAuthed(t, gw.URL+"/v1/chat/completions",
		`{"model":"@quality:m","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, b)
	}
	if *winner != "strong" {
		t.Fatalf("winner = %q, want strong (capability 95 over 40)", *winner)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(out.Choices[0].Message.Content, "strong") {
		t.Fatalf("content = %q", out.Choices[0].Message.Content)
	}
}

func TestQualityHeaderAlsoSelects(t *testing.T) { // X-OmniFusion-Strategy: quality
	gw, _, winner := newQualityFixture(t)
	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testGatewayToken)
	req.Header.Set("X-OmniFusion-Strategy", "quality")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || *winner != "strong" {
		t.Fatalf("status = %d winner = %q, want 200/strong", resp.StatusCode, *winner)
	}
}

func TestQualityWithoutFeedKeepsRegistryOrder(t *testing.T) { // 无能力数据=注册序
	gw, s, winner := newQualityFixture(t)
	s.router.Capability = nil // 摘除能力源：@quality 退化为注册序（weak 在前）
	resp := postAuthed(t, gw.URL+"/v1/chat/completions",
		`{"model":"@quality:m","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, b)
	}
	if *winner != "weak" {
		t.Fatalf("winner = %q, want weak (registry order when no capability data)", *winner)
	}
}
