package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/config"
	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/security"
	"github.com/swsgbl/omnifusion/internal/store"
)

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func findProvider(t *testing.T, providers []provider.Provider, id string) provider.Provider {
	t.Helper()
	for _, p := range providers {
		if p.Name() == id {
			return p
		}
	}
	t.Fatalf("provider %q not built", id)
	return nil
}

func translatedAuth(t *testing.T, p provider.Provider) string {
	t.Helper()
	call, err := p.Translate(context.Background(), &schema.UnifiedRequest{
		Model:    "m",
		Messages: []schema.Message{{Role: "user", Content: schema.NewTextContent("hi")}},
	})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	return call.Header.Get("Authorization")
}

func TestBuildRouterKeyringBeatsEnv(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	kr, err := security.Open("")
	if err != nil {
		t.Fatalf("open keyring: %v", err)
	}

	ct, err := kr.Encrypt([]byte("kr-key-123"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if err := st.SetConnection("openrouter", ct, ""); err != nil {
		t.Fatalf("SetConnection: %v", err)
	}
	t.Setenv("OPENROUTER_API_KEY", "env-key-456")

	r, _ := buildRouter(&config.Config{}, discardLog(), st, kr)
	p := findProvider(t, r.Providers, "openrouter")
	if got := translatedAuth(t, p); got != "Bearer kr-key-123" {
		t.Errorf("Authorization = %q, want keyring key", got)
	}
}

func TestBuildRouterEnvFallback(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	kr, err := security.Open("")
	if err != nil {
		t.Fatalf("open keyring: %v", err)
	}

	t.Setenv("OPENROUTER_API_KEY", "env-key-456")

	r, _ := buildRouter(&config.Config{}, discardLog(), st, kr)
	p := findProvider(t, r.Providers, "openrouter")
	if got := translatedAuth(t, p); got != "Bearer env-key-456" {
		t.Errorf("Authorization = %q, want env fallback", got)
	}
}
