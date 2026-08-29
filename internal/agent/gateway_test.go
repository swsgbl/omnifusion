// gateway_test.go 覆盖 GatewayView：dashboard API 解码口径与错误路径
// （401/网络不可达）。假网关用 httptest 回真实 JSON 形状。
package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newFakeGateway 回固定 dashboard API JSON；记录收到的 Authorization。
func newFakeGateway(t *testing.T, status int) (*httptest.Server, *string) {
	t.Helper()
	var auth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
			return
		}
		switch r.URL.Path {
		case "/dashboard/api/providers":
			_, _ = w.Write([]byte(`{"providers":[{"name":"groq","models":6,"latency_ms":245.5,` +
				`"success_rate":0.98,"last_success_at":"2026-08-28T02:00:00Z","cooldowns":` +
				`[{"scope":"model","model":"llama-4","until":"2026-08-28T03:00:00Z","reason":"quota rpd exhausted"}]}],` +
				`"models_total":6}`))
		case "/dashboard/api/keys":
			_, _ = w.Write([]byte(`{"keys":[{"provider":"groq","source":"env:GROQ_API_KEY"},` +
				`{"provider":"openrouter","source":"stored","label":"main","updated_at":"2026-08-20 10:00"}]}`))
		case "/dashboard/api/audit":
			_, _ = w.Write([]byte(`{"requests":[{"id":9,"ts":"2026-08-29T01:00:00Z","endpoint":"chat",` +
				`"model":"m","provider":"groq","status":200,"tokens_in":4,"tokens_out":2,` +
				`"latency_ms":120.5,"ttft_ms":-1,"cache_hit":false}]}`))
		case "/dashboard/api/usage":
			_, _ = w.Write([]byte(`{"usage":[{"provider":"groq","rpm":3,"rpd":120,"tpm":5000,"tpd":90000,` +
				`"limits":{"rpm":30,"rpd":14400,"tpm":0,"tpd":0},"headroom":0.9}],"cache_entries":7}`))
		case "/dashboard/api/whoami":
			_, _ = w.Write([]byte(`{"kind":"scoped","scopes":["route"]}`))
		case "/dashboard/api/models":
			_, _ = w.Write([]byte(`{"models":[{"provider":"groq","id":"llama-4","context_window":131072}]}`))
		case "/dashboard/api/health":
			_, _ = w.Write([]byte(`{"status":"ok","version":"test","providers":2,"active_cooldowns":1}`))
		case "/dashboard/api/route/status":
			_, _ = w.Write([]byte(`{"pinned":"groq","until":"2026-08-29T00:00:00Z","active_cooldowns":{"groq":1}}`))
		case "/dashboard/api/combos":
			_, _ = w.Write([]byte(`{"combos":[{"name":"cheap","stages":["session_dedup"],"compress":true}],"default_combo":"cheap"}`))
		default:
			if strings.HasPrefix(r.URL.Path, "/dashboard/api/route/") ||
				r.URL.Path == "/dashboard/api/compression/default" {
				_, _ = w.Write([]byte(`{"pinned":"groq","until":"2026-08-29T00:00:00Z","cleared":0,"default_combo":""}`))
				return
			}
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(up.Close)
	return up, &auth
}

func TestGatewayViewDecodesDashboardAPI(t *testing.T) {
	up, auth := newFakeGateway(t, http.StatusOK)
	g := NewGatewayView(up.URL, "tok-1", nil)
	ctx := context.Background()

	ps, err := g.Providers(ctx)
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	if len(ps.Providers) != 1 || ps.Providers[0].Name != "groq" || ps.Providers[0].Models != 6 {
		t.Fatalf("providers = %+v", ps.Providers)
	}
	if ps.Providers[0].LatencyMS != 245.5 || ps.Providers[0].SuccessRate != 0.98 {
		t.Errorf("scoring fields = %+v", ps.Providers[0])
	}
	if ps.Providers[0].LastSuccessAt == nil || *ps.Providers[0].LastSuccessAt != "2026-08-28T02:00:00Z" {
		t.Errorf("last_success_at = %v", ps.Providers[0].LastSuccessAt)
	}
	if len(ps.Providers[0].Cooldowns) != 1 || ps.Providers[0].Cooldowns[0].Model != "llama-4" {
		t.Errorf("cooldowns = %+v", ps.Providers[0].Cooldowns)
	}
	if ps.ModelsTotal != 6 {
		t.Errorf("models_total = %d, want 6", ps.ModelsTotal)
	}

	keys, err := g.Keys(ctx)
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	if len(keys) != 2 || keys[0].Source != "env:GROQ_API_KEY" || keys[1].Label != "main" {
		t.Fatalf("keys = %+v", keys)
	}

	us, err := g.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if len(us.Usage) != 1 || us.Usage[0].RPM != 3 || us.Usage[0].Limits.RPM != 30 || us.Usage[0].Headroom != 0.9 {
		t.Fatalf("usage = %+v", us.Usage)
	}
	if us.CacheEntries != 7 {
		t.Errorf("cache_entries = %d, want 7", us.CacheEntries)
	}
	if *auth != "Bearer tok-1" {
		t.Errorf("Authorization = %q, want Bearer tok-1", *auth)
	}
}

func TestGatewayViewSurfacesGatewayErrors(t *testing.T) {
	up, _ := newFakeGateway(t, http.StatusUnauthorized)
	g := NewGatewayView(up.URL, "wrong", nil)
	if _, err := g.Providers(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("401 error = %v, want HTTP 401 detail", err)
	}

	g = NewGatewayView("http://127.0.0.1:1", "tok", nil)
	if _, err := g.Usage(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "gateway unreachable") {
		t.Errorf("unreachable error = %v, want gateway unreachable", err)
	}
}

func TestGatewayViewControlEndpoints(t *testing.T) {
	up, _ := newFakeGateway(t, http.StatusOK)
	g := NewGatewayView(up.URL, "tok-1", nil)
	ctx := context.Background()

	if wm, err := g.Whoami(ctx); err != nil || wm.Kind != "scoped" || len(wm.Scopes) != 1 {
		t.Fatalf("Whoami = %+v %v", wm, err)
	}
	if ms, err := g.Models(ctx); err != nil || len(ms) != 1 || ms[0].ContextWindow != 131072 {
		t.Fatalf("Models = %+v %v", ms, err)
	}
	if h, err := g.Health(ctx); err != nil || h.Providers != 2 || h.ActiveCooldowns != 1 {
		t.Fatalf("Health = %+v %v", h, err)
	}
	if rs, err := g.RouteStatus(ctx); err != nil || rs.Pinned != "groq" || rs.ActiveCooldowns["groq"] != 1 {
		t.Fatalf("RouteStatus = %+v %v", rs, err)
	}
	if cs, err := g.Combos(ctx); err != nil || len(cs.Combos) != 1 || cs.Combos[0].Stages[0] != "session_dedup" || cs.DefaultCombo != "cheap" {
		t.Fatalf("Combos = %+v %v", cs, err)
	}
	if pr, err := g.RoutePin(ctx, "groq", 120); err != nil || pr.Pinned != "groq" || pr.Until == nil {
		t.Fatalf("RoutePin = %+v %v", pr, err)
	}
	if cr, err := g.ClearCooldowns(ctx, "groq"); err != nil || cr.Cleared != 0 {
		t.Fatalf("ClearCooldowns = %+v %v", cr, err)
	}
	if dc, err := g.SetDefaultCombo(ctx, "cheap"); err != nil || dc.DefaultCombo != "" {
		t.Fatalf("SetDefaultCombo = %+v %v", dc, err)
	}
}
