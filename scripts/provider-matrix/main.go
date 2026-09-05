// Command provider-matrix renders the built-in provider registry as a
// public Markdown matrix. Keeping the generator in the repository prevents
// PROVIDERS.md from drifting from the YAML declarations.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/swsgbl/omnifusion/internal/provider/registry"
)

func main() {
	date := flag.String("date", "", "verification/regeneration date in YYYY-MM-DD form (required)")
	out := flag.String("out", "", "output file; omit to write to stdout")
	flag.Parse()

	if strings.TrimSpace(*date) == "" {
		fmt.Fprintln(os.Stderr, "provider-matrix: -date is required (YYYY-MM-DD)")
		os.Exit(2)
	}
	entries, err := registry.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "provider-matrix: load registry: %v\n", err)
		os.Exit(1)
	}

	rendered := renderMatrix(*date, entries)
	if *out == "" {
		fmt.Print(rendered)
		return
	}
	if err := os.WriteFile(*out, []byte(rendered), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "provider-matrix: write %s: %v\n", *out, err)
		os.Exit(1)
	}
}

func renderMatrix(date string, entries []registry.Entry) string {
	var b strings.Builder
	b.WriteString("# Built-in Provider Matrix\n\n")
	b.WriteString("Last regenerated: " + mdEscape(date) + "\n\n")
	fmt.Fprintf(
		&b,
		"OmniFusion currently ships %d built-in provider declarations and accepts custom OpenAI-compatible, Anthropic, and Gemini provider declarations. ",
		len(entries),
	)
	b.WriteString("The goal is continuous global free-provider aggregation; the table below describes what this repository currently ships. Upstream quotas and model availability change frequently, so always confirm the provider's current terms before production use.\n\n")
	b.WriteString("Regenerate this page after changing a declaration:\n\n")
	b.WriteString("```bash\ngo run ./scripts/provider-matrix -date YYYY-MM-DD -out PROVIDERS.md\n```\n\n")
	b.WriteString("| Provider | Protocol | Declared free tier / billing boundary | Declared window limits | Models | Capabilities | Key signup |\n")
	b.WriteString("|---|---|---|---|---|---|---|\n")
	for _, e := range entries {
		freeModels := 0
		for _, m := range e.Models {
			if m.PriceIn != nil && m.PriceOut != nil && *m.PriceIn == 0 && *m.PriceOut == 0 {
				freeModels++
			}
		}
		models := "-"
		if len(e.Models) > 0 {
			models = fmt.Sprintf("%d declared / %d explicit-free", len(e.Models), freeModels)
		}
		signup := "-"
		if e.SignupURL != "" {
			signup = fmt.Sprintf("[keys](%s)", e.SignupURL)
		}
		fmt.Fprintf(
			&b,
			"| %s | %s | %s | %s | %s | %s | %s |\n",
			mdEscape(e.DisplayName),
			protocolLabel(e.Kind),
			mdEscape(firstNonEmpty(e.FreeTier, "Not declared")),
			mdEscape(rateLimits(e.RateLimits)),
			models,
			mdEscape(capabilities(e)),
			signup,
		)
	}
	b.WriteString("\nThe source of truth is `internal/provider/registry/providers/`. A row is a routing declaration, not a promise that every account, region, or model is currently usable. Providers without a recurring free tier remain useful for BYOK routing and custom fallback chains.\n")
	return b.String()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func protocolLabel(kind string) string {
	switch kind {
	case registry.KindAnthropic:
		return "Anthropic"
	case registry.KindGemini:
		return "Gemini"
	default:
		return "OpenAI-compatible"
	}
}

func rateLimits(l registry.RateLimitsDecl) string {
	var parts []string
	if l.RPM > 0 {
		parts = append(parts, fmt.Sprintf("%d RPM", l.RPM))
	}
	if l.RPD > 0 {
		parts = append(parts, fmt.Sprintf("%s RPD", compact(int64(l.RPD))))
	}
	if l.TPM > 0 {
		parts = append(parts, fmt.Sprintf("%s TPM", compact(l.TPM)))
	}
	if l.TPD > 0 {
		parts = append(parts, fmt.Sprintf("%s TPD", compact(l.TPD)))
	}
	if len(parts) == 0 {
		return "Provider/account-specific"
	}
	return strings.Join(parts, ", ")
}

func compact(v int64) string {
	switch {
	case v%1_000_000 == 0:
		return strconv.FormatInt(v/1_000_000, 10) + "M"
	case v%1_000 == 0:
		return strconv.FormatInt(v/1_000, 10) + "K"
	default:
		return strconv.FormatInt(v, 10)
	}
}

func capabilities(e registry.Entry) string {
	parts := []string{
		"in: " + strings.Join(e.Capabilities.Input, "/"),
		"out: " + strings.Join(e.Capabilities.Output, "/"),
	}
	if len(e.Capabilities.Features) > 0 {
		parts = append(parts, strings.Join(e.Capabilities.Features, "/"))
	}
	return strings.Join(parts, "; ")
}

func mdEscape(v string) string {
	replacer := strings.NewReplacer("|", "\\|", "\n", " ")
	return replacer.Replace(v)
}
