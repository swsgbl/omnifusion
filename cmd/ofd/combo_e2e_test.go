package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/swsgbl/omnifusion/internal/config"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/server"
)

// comboUpstream 是 mock 上游：计数、记录最后请求体。
type comboUpstream struct {
	mu    sync.Mutex
	hits  int
	body  string
	model string
}

func (u *comboUpstream) serve(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.hits++
	u.body = string(b)
	var probe struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(b, &probe)
	u.model = probe.Model
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"m",`+
		`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
}

func (u *comboUpstream) snapshot() (int, string, string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits, u.body, u.model
}

// TestComboYAMLConfigTakesEffect 是 docs/05 4.7 的验收：YAML 声明的
// 路由组合（成员序 + 成员模型）与压缩组合绑定（per-path 压缩策略）
// 全链路生效——请求 "@combo:free-tier" 走成员 b、按成员模型发上游、
// 消息按绑定组合被压缩；纯路由组合不压缩；未知组合 400。
func TestComboYAMLConfigTakesEffect(t *testing.T) {
	yamlCfg := `
server:
  host: 127.0.0.1
  port: 20130
combos:
  routing:
    free-tier:
      members:
        - provider: b
          model: model-b-free
        - provider: a
          model: model-a-free
      compression: aggressive
    plain:
      members:
        - provider: a
          model: model-a-free
  compression:
    aggressive: [dedup, caveman]
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yamlCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	combos, comboPipes, err := buildCombos(cfg)
	if err != nil {
		t.Fatalf("buildCombos: %v", err)
	}

	upA, upB := &comboUpstream{}, &comboUpstream{}
	mkUp := func(u *comboUpstream) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(u.serve))
		t.Cleanup(s.Close)
		return s
	}
	srvA, srvB := mkUp(upA), mkUp(upB)
	adapter := func(name string, base string) provider.Provider {
		a, err := openai_compat.New(openai_compat.Spec{
			ProviderName: name, BaseURL: base + "/v1", APIKey: "k"})
		if err != nil {
			t.Fatalf("adapter %s: %v", name, err)
		}
		return a
	}

	router := &routing.Router{Providers: []provider.Provider{
		adapter("a", srvA.URL), adapter("b", srvB.URL)}}
	router.Combos = combos
	s := server.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	s.SetRouter(router)
	s.SetComboPipelines(comboPipes)
	s.SetGatewayToken("test-gw-key")
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)

	post := func(model string) *http.Response {
		t.Helper()
		// 5 条相同的冗长 user 消息 + 1 条收尾提问：dedup 折叠重复、
		// caveman 删填充词（尾 2 条受 recency 保护不动）。
		verbose := strings.Repeat(
			"In order to please basically review the quicksort implementation very carefully. ", 4)
		var msgs []string
		for i := 0; i < 5; i++ {
			msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":%q}`, verbose))
		}
		msgs = append(msgs, `{"role":"user","content":"Summarize."}`)
		body := fmt.Sprintf(`{"model":%q,"messages":[%s]}`, model, strings.Join(msgs, ","))
		req, err := http.NewRequest(http.MethodPost, gw.URL+"/v1/chat/completions",
			strings.NewReader(body))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer test-gw-key")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		return resp
	}

	// ① 绑定压缩的组合：消息被压缩 + 成员模型 + 只走成员 b
	resp := post("@combo:free-tier")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("free-tier status = %d, body = %s", resp.StatusCode, b)
	}
	hitsA, _, _ := upA.snapshot()
	hitsB, bodyB, modelB := upB.snapshot()
	if hitsB != 1 {
		t.Fatalf("成员 b 上游命中 %d 次, want 1", hitsB)
	}
	if hitsA != 0 {
		t.Errorf("非首成员 a 被命中 %d 次, want 0（b 成功无 failover）", hitsA)
	}
	if modelB != "model-b-free" {
		t.Errorf("上游 model = %q, want 成员模型 model-b-free", modelB)
	}
	// 每条 verbose 消息含 4 个 "quicksort"/"basically"：按消息数换算。
	if got := strings.Count(bodyB, "quicksort"); got != 8 {
		t.Errorf("dedup 后 quicksort 出现 %d 次, want 8（5 条折叠为 2 条 ×4/条）", got)
	}
	if got := strings.Count(bodyB, "basically"); got != 1*4 {
		t.Errorf("caveman 后 basically 出现 %d 次, want 4（仅 recency 保护的 1 条 ×4/条）", got)
	}

	// ② 纯路由组合：不压缩，模型仍按成员改写
	resp2 := post("@combo:plain")
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("plain status = %d", resp2.StatusCode)
	}
	hitsA2, bodyA, modelA := upA.snapshot()
	if hitsA2 != 1 || modelA != "model-a-free" {
		t.Errorf("plain 组合应走 a/model-a-free, got hits=%d model=%q", hitsA2, modelA)
	}
	if got := strings.Count(bodyA, "quicksort"); got != 5*4 {
		t.Errorf("无压缩绑定时 quicksort 应出现 20 次（5 条 ×4/条）, got %d", got)
	}

	// ③ 未知组合：400
	resp3 := post("@combo:ghost")
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusBadRequest {
		t.Errorf("未知组合 status = %d, want 400", resp3.StatusCode)
	}
}
