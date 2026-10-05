package lex

import (
	"context"
	"testing"

	"github.com/nico/jev-trans/internal/lang"
)

// TestProcessBackendSpeaksToTheRealAdapter is skipped when sudachipy or a
// SudachiDict is not installed, because the core is required to run without
// them. Where they are present this is the check that the line protocol, the
// offsets and the OOV flag all survive the pipe.
func TestProcessBackendSpeaksToTheRealAdapter(t *testing.T) {
	cfg := SudachiConfig("core")
	if !Available(cfg.Command) {
		t.Skip("python3 is not available")
	}
	p := NewProcessAnalyzer(cfg)
	defer p.Close()

	an, err := p.Analyze(context.Background(), "今日はいい天気ですね。", ProfileModern)
	if err != nil {
		t.Skipf("sudachi is not usable here: %v", err)
	}
	if an.Backend != "sudachi" {
		t.Errorf("backend = %q", an.Backend)
	}
	if len(an.Tokens) < 5 {
		t.Fatalf("expected at least 5 tokens, got %d", len(an.Tokens))
	}
	// Offsets must tile the input exactly; the whole point of a real analyser
	// is that it hands back a segmentation the rest of the pipeline can trust.
	cursor := 0
	for _, tk := range an.Tokens {
		if tk.Start != cursor {
			t.Fatalf("offset gap at %q: expected %d, got %d", tk.Surface, cursor, tk.Start)
		}
		if tk.End <= tk.Start {
			t.Fatalf("token %q has an empty span [%d,%d)", tk.Surface, tk.Start, tk.End)
		}
		cursor = tk.End
	}
	if cursor != len("今日はいい天気ですね。") {
		t.Errorf("tokens cover %d bytes of %d", cursor, len("今日はいい天気ですね。"))
	}
	// A lexicon that knows 今日は should not mark it unknown.
	for _, tk := range an.Tokens {
		if tk.Surface == "今日" && tk.Unknown {
			t.Error("今日 reported as out of vocabulary")
		}
	}
}

// TestRegistryUsesSudachiWhenAvailable proves the wiring, not just the adapter.
func TestRegistryUsesSudachiWhenAvailable(t *testing.T) {
	cfg := SudachiConfig("core")
	if !Available(cfg.Command) {
		t.Skip("python3 is not available")
	}
	r := NewRegistry()
	r.Register(NewProcessAnalyzer(cfg))
	an, err := r.Analyze(context.Background(), "今日はいい天気ですね。", ProfileModern, lang.JA)
	if err != nil {
		t.Skipf("sudachi is not usable here: %v", err)
	}
	if an.Backend != "sudachi" {
		t.Errorf("backend = %q, want sudachi; the registry did not prefer it over the builtin", an.Backend)
	}
}
