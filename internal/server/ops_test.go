// ops_test.go 覆盖运维写路径：隔离清除与默认压缩组合。
package server

import (
	"encoding/json"
	"github.com/swsgbl/omnifusion/internal/compression"
	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/store"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestCooldownsClear 验证隔离清除：预置 connection 冷却 → providers
// API 可见 → clear 后消失（store + 内存）。
func TestCooldownsClear(t *testing.T) {
	gw, s, st, _, _ := newControlFixture(t)
	routeTok := DeriveMCPToken(testGatewayToken, []string{ScopeRoute})

	if err := st.UpsertCooldown(store.Cooldown{
		ScopeType: "connection", Provider: "alpha",
		Until: time.Now().Add(10 * time.Minute), Reason: "rate_limit",
	}); err != nil {
		t.Fatalf("upsert cooldown: %v", err)
	}
	// 重启语义：Isolation 从 store 恢复，故这里直接挂新状态机。
	iso, err := routing.NewIsolation(st, nil)
	if err != nil {
		t.Fatalf("NewIsolation: %v", err)
	}
	s.router.Isolation = iso

	resp := apiCall(t, http.MethodGet, gw.URL+"/dashboard/api/providers", testGatewayToken, "")
	var provs struct {
		Providers []struct {
			Name      string `json:"name"`
			Cooldowns []struct {
				Reason string `json:"reason"`
			} `json:"cooldowns"`
		} `json:"providers"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&provs) != nil {
		resp.Body.Close()
		t.Fatalf("providers: %d", resp.StatusCode)
	}
	resp.Body.Close()
	if len(provs.Providers[0].Cooldowns) != 1 {
		t.Fatalf("expected 1 cooldown on alpha, got %+v", provs.Providers[0])
	}

	resp = apiCall(t, http.MethodPost, gw.URL+"/dashboard/api/route/cooldowns/clear", routeTok, `{"provider":"alpha"}`)
	defer resp.Body.Close()
	var cleared struct {
		Cleared int `json:"cleared"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&cleared) != nil {
		t.Fatalf("clear: %d", resp.StatusCode)
	}
	if cleared.Cleared != 1 {
		t.Fatalf("cleared = %d, want 1", cleared.Cleared)
	}
	if blocked, _ := iso.Block("alpha"); blocked {
		t.Fatal("alpha still blocked after clear")
	}
}

// TestCompressionDefault 验证默认压缩组合：设置后无指令请求按组合
// 分发（dispatchOptions 返回组合名）；未知组合 400；清除后恢复。
func TestCompressionDefault(t *testing.T) {
	gw, s, _, _, _ := newControlFixture(t)
	s.SetComboPipelines(map[string]*compression.Pipeline{
		"light": compression.NewPipeline(nil, compression.NewDedupStage(compression.DedupConfig{})),
	})
	compTok := DeriveMCPToken(testGatewayToken, []string{ScopeCompression})

	resp := apiCall(t, http.MethodPost, gw.URL+"/dashboard/api/compression/default", testGatewayToken, `{"combo":"ghost"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown combo = %d, want 400", resp.StatusCode)
	}

	resp = apiCall(t, http.MethodPost, gw.URL+"/dashboard/api/compression/default", compTok, `{"combo":"light"}`)
	var out struct {
		DefaultCombo string `json:"default_combo"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil {
		resp.Body.Close()
		t.Fatalf("set default: %d", resp.StatusCode)
	}
	resp.Body.Close()
	if out.DefaultCombo != "light" {
		t.Fatalf("default_combo = %q, want light", out.DefaultCombo)
	}

	dreq := &schema.UnifiedRequest{
		Model:    "m",
		Messages: []schema.Message{{Role: "user", Content: schema.NewTextContent("hi")}},
	}
	_, combo, _, err := s.dispatchOptions(httptest.NewRequest(http.MethodPost, gw.URL, nil), dreq)
	if err != nil {
		t.Fatalf("dispatchOptions: %v", err)
	}
	if combo != "light" {
		t.Fatalf("dispatch combo = %q, want light (default applied)", combo)
	}

	resp = apiCall(t, http.MethodPost, gw.URL+"/dashboard/api/compression/default", compTok, `{"combo":""}`)
	resp.Body.Close()
	if got := s.defaultCombo(); got != "" {
		t.Fatalf("default_combo after clear = %q, want empty", got)
	}
}
