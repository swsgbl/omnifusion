package openai_compat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
)

func TestListModelsLive(t *testing.T) {
	var sawAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		// OpenRouter 形 context_length 与 Groq 形 context_window 并存
		io.WriteString(w, `{"data":[{"id":"a","context_length":4096},`+
			`{"id":"b","context_window":8192},{"id":""}]}`)
	}))
	defer up.Close()

	a, err := New(Spec{ProviderName: "x", BaseURL: up.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if sawAuth != "Bearer k" {
		t.Errorf("auth = %q, want Bearer k", sawAuth)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v, want 2 (blank id dropped)", models)
	}
	if models[0].ID != "a" || models[0].ContextWindow != 4096 {
		t.Errorf("models[0] = %+v", models[0])
	}
	if models[1].ID != "b" || models[1].ContextWindow != 8192 {
		t.Errorf("models[1] = %+v (context_window fallback)", models[1])
	}
}

func TestListModelsNon2xx(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"boom"}`)
	}))
	defer up.Close()

	a, err := New(Spec{ProviderName: "x", BaseURL: up.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = a.ListModels(context.Background())
	ue, ok := provider.IsUpstream(err)
	if !ok {
		t.Fatalf("err = %v, want UpstreamError", err)
	}
	if ue.Status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", ue.Status)
	}
}

func TestListModelsEmptyData(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[]}`)
	}))
	defer up.Close()

	a, err := New(Spec{ProviderName: "x", BaseURL: up.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := a.ListModels(context.Background()); err == nil {
		t.Fatal("empty catalog must be an error, not a silent wipe")
	}
}
