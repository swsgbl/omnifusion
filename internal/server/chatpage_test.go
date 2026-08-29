package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
)

func TestDashboardChatPageAndHTMLAlias(t *testing.T) {
	gw, _, _ := newQualityFixture(t)
	for _, path := range []string{"/dashboard/chat", "/dashboard/chat.html", "/dashboard/providers.html"} {
		req, _ := http.NewRequest(http.MethodGet, gw.URL+path+"?key="+testGatewayToken, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("%s = %d %s", path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if !strings.Contains(string(b), "chat") && !strings.Contains(string(b), "nav") {
			t.Fatalf("%s body unexpected", path)
		}
	}
}

func TestBareQualityAutoDispatch(t *testing.T) { // model=@quality → 各家最强模型逐尝试
	gw, _, winner := newQualityFixture(t)
	resp := postAuthed(t, gw.URL+"/v1/chat/completions",
		`{"model":"@quality","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, b)
	}
	if *winner != "strong" {
		t.Fatalf("winner = %q, want strong (best model capability 95)", *winner)
	}
}

func TestUpstream403CarriesRegionHint(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":"blocked"}`)
	}))
	t.Cleanup(up.Close)
	a, err := openai_compat.New(openai_compat.Spec{ProviderName: "blocked", BaseURL: up.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	s.SetRouter(&routing.Router{Providers: []provider.Provider{a}})
	gw := httptest.NewServer(s.Handler())
	t.Cleanup(gw.Close)

	resp := postAuthed(t, gw.URL+"/v1/chat/completions",
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(body.Error.Message, "region-blocked") {
		t.Fatalf("message = %q, want region hint", body.Error.Message)
	}
}
