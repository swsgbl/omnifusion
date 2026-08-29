package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
)

// getAuthed 以合法网关 key 发 GET（postAuthed 只覆盖 POST）。
func getAuthed(t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testGatewayToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	return resp
}

func TestModelsRequiresAuth(t *testing.T) {
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()

	resp, err := http.Get(gw.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bare status = %d, want 401", resp.StatusCode)
	}
}

func TestModelsListShape(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[
			{"id":"m-a","context_length":8192},
			{"id":"m-b"}
		]}`)
	}))
	defer upstream.Close()

	adapter, err := openai_compat.New(openai_compat.Spec{
		ProviderName: "mock",
		BaseURL:      upstream.URL + "/v1",
		APIKey:       "sk-test",
	})
	if err != nil {
		t.Fatalf("openai_compat.New: %v", err)
	}
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	catalog := routing.NewCatalog(
		[]provider.Provider{adapter}, nil, nil, nil, nil,
	)
	if changed := catalog.Sync(context.Background()); changed != 1 {
		t.Fatalf("catalog Sync changed = %d, want 1", changed)
	}
	s.SetCatalog(catalog)

	gw := httptest.NewServer(s.Handler())
	defer gw.Close()

	resp := getAuthed(t, gw.URL+"/v1/models")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	var payload struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Object != "list" {
		t.Errorf("object = %q, want list", payload.Object)
	}
	if len(payload.Data) != 2 {
		t.Fatalf("data = %+v, want 2 entries", payload.Data)
	}
	for i, want := range []string{"m-a", "m-b"} {
		got := payload.Data[i]
		if got.ID != want || got.Object != "model" || got.OwnedBy != "mock" {
			t.Errorf("data[%d] = %+v, want id %s / model / mock", i, got, want)
		}
	}
}

func TestModelsEmptyCatalog(t *testing.T) {
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil))
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()

	resp := getAuthed(t, gw.URL+"/v1/models")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var payload struct {
		Object string           `json:"object"`
		Data   []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Object != "list" || len(payload.Data) != 0 {
		t.Errorf("payload = %+v, want empty list", payload)
	}
}
