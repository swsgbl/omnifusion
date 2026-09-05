package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider/registry"
)

func TestRenderMatrixUsesRegistry(t *testing.T) {
	entries, err := registry.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := renderMatrix("2026-09-05", entries)
	if !strings.Contains(got, fmt.Sprintf("%d built-in provider declarations", len(entries))) {
		t.Fatalf("provider count missing or wrong:\n%s", got)
	}
	if gotRows := strings.Count(got, "\n| "); gotRows != len(entries)+1 {
		t.Fatalf("table rows = %d, want %d", gotRows-1, len(entries))
	}
	if !strings.Contains(got, "| OpenRouter | OpenAI-compatible |") {
		t.Fatalf("OpenRouter row missing or malformed:\n%s", got)
	}
	if !strings.Contains(got, "20 RPM, 50 RPD") {
		t.Fatalf("OpenRouter conservative limits missing:\n%s", got)
	}
	if !strings.Contains(got, "| Google Gemini | Gemini |") {
		t.Fatalf("Gemini native protocol row missing:\n%s", got)
	}
}

func TestMarkdownEscapingAndLimitFormatting(t *testing.T) {
	entry := registry.Entry{
		DisplayName: "Pipe | Test",
		Kind:        registry.KindOpenAICompat,
		FreeTier:    "trial | tier",
		RateLimits:  registry.RateLimitsDecl{RPM: 20, RPD: 1000, TPM: 30000, TPD: 1000000},
		Capabilities: registry.CapabilityDecl{
			Input: []string{"text"}, Output: []string{"text"},
		},
	}
	got := renderMatrix("2026-09-05|x", []registry.Entry{entry})
	if !strings.Contains(got, "Pipe \\| Test") || !strings.Contains(got, "trial \\| tier") {
		t.Fatalf("Markdown pipes not escaped:\n%s", got)
	}
	if !strings.Contains(got, "20 RPM, 1K RPD, 30K TPM, 1M TPD") {
		t.Fatalf("limits not compacted:\n%s", got)
	}
}
