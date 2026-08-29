package store

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestConnectionsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	cipher := []byte{0x01, 0xde, 0xad, 0xbe, 0xef}
	if err := s.SetConnection("openrouter", cipher, "main"); err != nil {
		t.Fatalf("SetConnection: %v", err)
	}
	got, err := s.GetConnection("openrouter")
	if err != nil {
		t.Fatalf("GetConnection: %v", err)
	}
	if got == nil {
		t.Fatal("GetConnection returned nil for existing provider")
	}
	if !bytes.Equal(got.KeyCipher, cipher) || got.Label != "main" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if got.UpdatedAt == "" {
		t.Error("updated_at should be stamped")
	}
}

func TestConnectionsOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := s.SetConnection("groq", []byte("old"), ""); err != nil {
		t.Fatalf("set old: %v", err)
	}
	if err := s.SetConnection("groq", []byte("new"), "work"); err != nil {
		t.Fatalf("set new: %v", err)
	}
	got, err := s.GetConnection("groq")
	if err != nil || got == nil {
		t.Fatalf("get: %v", err)
	}
	if string(got.KeyCipher) != "new" || got.Label != "work" {
		t.Errorf("overwrite failed: %+v", got)
	}
}

func TestConnectionsMissingIsNil(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	got, err := s.GetConnection("nope")
	if err != nil {
		t.Fatalf("GetConnection: %v", err)
	}
	if got != nil {
		t.Errorf("missing provider should return nil, got %+v", got)
	}
}

func TestConnectionsListAndDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	for _, p := range []string{"groq", "openrouter"} {
		if err := s.SetConnection(p, []byte("c-"+p), ""); err != nil {
			t.Fatalf("set %s: %v", p, err)
		}
	}
	list, err := s.ListConnections()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[0].Provider != "groq" || list[1].Provider != "openrouter" {
		t.Fatalf("list = %+v", list)
	}

	if err := s.DeleteConnection("groq"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.DeleteConnection("groq"); err != nil {
		t.Fatalf("double delete should be no-op: %v", err)
	}
	list, err = s.ListConnections()
	if err != nil || len(list) != 1 {
		t.Fatalf("after delete list = %+v, %v", list, err)
	}
}
