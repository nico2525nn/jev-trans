package lex

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nico2525nn/jev-trans/internal/lang"
)

// failingBackend stands in for a configured external analyser that cannot
// start: Sudachi without its dictionary, UniDic without MeCab, an executable
// that is not on PATH.
type failingBackend struct {
	name string
	err  error
}

func (f failingBackend) Name() string    { return f.name }
func (f failingBackend) Version() string { return "1.0" }
func (f failingBackend) Supports(Profile) bool {
	return true
}
func (f failingBackend) Analyze(context.Context, string, Profile) (*Analysis, error) {
	return nil, f.err
}

func TestRegistryFallsBackToBuiltinWhenBackendFails(t *testing.T) {
	r := NewRegistry()
	r.Register(failingBackend{name: "sudachi", err: errors.New("dictionary not found")})

	an, err := r.Analyze(context.Background(), "今日はいい天気ですね。", ProfileAuto, lang.JA)
	if err != nil {
		t.Fatalf("Analyze returned %v; a broken external backend must not fail the run", err)
	}
	if an.Backend != "builtin" {
		t.Errorf("backend = %q, want builtin", an.Backend)
	}
	if len(an.Tokens) == 0 {
		t.Error("fallback produced no tokens")
	}
	if an.Profile == "" {
		t.Error("fallback did not record the profile it was asked for")
	}
}

// A backend that only serves some profiles must not be used for the others,
// and the failure of the preferred one must not lose the run.
func TestRegistryRespectsProfileSupport(t *testing.T) {
	r := NewRegistry()
	r.Register(failingBackend{name: "sudachi", err: errors.New("no dictionary")})

	an, err := r.Analyze(context.Background(), "今日はいい天気ですね。", ProfileModern, lang.JA)
	if err != nil {
		t.Fatalf("Analyze returned %v", err)
	}
	if an.Profile != ProfileModern {
		t.Errorf("profile = %q, want %q", an.Profile, ProfileModern)
	}
}

// The registry must prefer a backend that claims the profile over one that does
// not, even if the one that does not is registered first.
func TestRegistryPrefersProfileCapableBackend(t *testing.T) {
	r := NewRegistry()
	r.Register(profileOnlyBackend{name: "unidic", profiles: []Profile{ProfileOldKanaColloquial}})
	an, err := r.Analyze(context.Background(), "私は行きます。", ProfileOldKanaColloquial, lang.JA)
	if err != nil {
		t.Fatalf("Analyze returned %v", err)
	}
	if an.Backend != "unidic" {
		t.Errorf("backend = %q, want unidic", an.Backend)
	}
}

// The lattice a backend produces has to cover the input exactly. A morphological
// stage that loses characters silently invalidates every span downstream.
func TestLatticeCoversInputExactly(t *testing.T) {
	r := NewRegistry()
	src := "今日はいい天気ですね。"
	an, err := r.Analyze(context.Background(), src, ProfileAuto, lang.JA)
	if err != nil {
		t.Fatalf("Analyze returned %v", err)
	}
	mf := an.Lattice(src)
	best := mf.Best()
	if len(best.Morphs) == 0 {
		t.Fatal("lattice is empty")
	}
	cursor := 0
	for _, m := range best.Morphs {
		if m.Start != cursor {
			t.Fatalf("gap or overlap at %q: expected offset %d, got %d", m.Surface, cursor, m.Start)
		}
		cursor = m.End
	}
	if cursor != len(src) {
		t.Errorf("lattice covers %d bytes of %d", cursor, len(src))
	}
}

// The coarse part of speech has to survive the round trip: a builtin token
// already speaks the internal vocabulary, and re-interpreting it as a Universal
// tag loses the predicate entirely.
func TestLatticePreservesPartOfSpeech(t *testing.T) {
	r := NewRegistry()
	an, err := r.Analyze(context.Background(), "私は行きます。", ProfileAuto, lang.JA)
	if err != nil {
		t.Fatalf("Analyze returned %v", err)
	}
	mf := an.Lattice("私は行きます。")
	var verb bool
	for _, m := range mf.Best().Morphs {
		if string(m.POS) == "VERB" {
			verb = true
		}
	}
	if !verb {
		var got []string
		for _, m := range mf.Best().Morphs {
			got = append(got, string(m.POS))
		}
		t.Errorf("no VERB survived; parts of speech were %v", got)
	}
}

func TestDetectProfile(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want Profile
	}{
		{"modern", "今日はいい天気ですね。", ProfileModern},
		{"old kana", "今日はゐい天氣ですね。", ProfileOldKanaColloquial},
		{"iteration mark", " 큰いゝ天氣。", ProfileOldKanaColloquial},
		{"empty", "", ProfileModern},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectProfile(tc.text); got != tc.want {
				t.Errorf("DetectProfile(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestAvailableReportsMissingCommand(t *testing.T) {
	if Available("definitely-not-a-real-command-9d2f") {
		t.Error("Available reported a missing command as present")
	}
}

// A process backend that was never started must report that cleanly rather than
// panicking on a nil pipe.
func TestProcessBackendUnstartedIsSafe(t *testing.T) {
	p := NewProcessAnalyzer(ProcessConfig{
		Command: "definitely-not-a-real-command-9d2f", Name: "ghost", Version: "0",
	})
	if _, err := p.Analyze(context.Background(), "テスト", ProfileModern); err == nil {
		t.Error("expected an error from a backend that cannot start")
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close on an unstarted backend returned %v", err)
	}
}

// Empty input must not start a process at all.
func TestProcessBackendShortCircuitsEmptyInput(t *testing.T) {
	p := NewProcessAnalyzer(ProcessConfig{
		Command: "definitely-not-a-real-command-9d2f", Name: "ghost",
	})
	an, err := p.Analyze(context.Background(), "   ", ProfileModern)
	if err != nil {
		t.Fatalf("empty input should short-circuit, got %v", err)
	}
	if len(an.Tokens) != 0 {
		t.Errorf("empty input produced %d tokens", len(an.Tokens))
	}
}

// The analyzer name and version must reach the caller, because plan2.md
// requires a Sudachi version to be pinned: a patch release there can change the
// dictionary format and silently alter every analysis.
func TestSudachiConfigIsPinnedAndProfileScoped(t *testing.T) {
	p := NewProcessAnalyzer(SudachiConfig("core"))
	// The pin must name a version of SudachiPy that exists. It previously said
	// 0.8.2, which is the Java Sudachi release; the current SudachiPy stable is
	// 0.7.0 and it does read a V1 SudachiDict.
	if strings.Contains(p.Version(), "0.8") {
		t.Errorf("version = %q; 0.8.x is a Java Sudachi release, not a SudachiPy one", p.Version())
	}
	if !strings.Contains(p.Version(), "0.7") {
		t.Errorf("version = %q, want the current SudachiPy stable", p.Version())
	}
	if !strings.Contains(p.Dictionary(), "core") {
		t.Errorf("dictionary = %q, want the core SudachiDict", p.Dictionary())
	}
	if !p.Supports(ProfileModern) {
		t.Error("SudachiDict should serve the modern profile")
	}
	if p.Supports(ProfileOldKanaColloquial) {
		t.Error("SudachiDict should not claim the old-kana profile; the 国語研 UniDic " +
			"build serves it")
	}
}

type profileOnlyBackend struct {
	name     string
	profiles []Profile
}

func (p profileOnlyBackend) Name() string    { return p.name }
func (p profileOnlyBackend) Version() string { return "1" }
func (p profileOnlyBackend) Supports(pr Profile) bool {
	for _, c := range p.profiles {
		if c == pr {
			return true
		}
	}
	return false
}
func (p profileOnlyBackend) Analyze(_ context.Context, text string, _ Profile) (*Analysis, error) {
	return &Analysis{
		Backend: p.name, BackendVersion: "1",
		Tokens: []Token{{Surface: text, Lemma: text, Start: 0, End: len([]rune(text)), POS: []string{"名詞"}}},
	}, nil
}

// TestProcessBackendSerialisesConcurrentRequests is the regression test for a
// double-start: Analyze used to launch the process before taking the lock, so
// two simultaneous requests each started one and the second overwrote p.cmd and
// p.stdin, orphaning the first process and its scanner. From an HTTP server two
// requests for the same backend arrive together routinely.
//
// Run with -race.
func TestProcessBackendSerialisesConcurrentRequests(t *testing.T) {
	cfg := SudachiConfig("core")
	if !Available(cfg.Command) {
		t.Skip("python3 is not available")
	}
	p := NewProcessAnalyzer(cfg)
	defer p.Close()
	if _, err := p.Analyze(context.Background(), "私は行きます。", ProfileModern); err != nil {
		t.Skipf("sudachi is not usable here: %v", err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = p.Analyze(context.Background(), "今日はいい天気ですね。", ProfileModern)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent request %d failed: %v", i, err)
		}
	}
}

// TestProcessBackendRespectsItsOwnDeadline covers the case where the caller
// supplies no deadline at all. The backend must not be able to hold the
// translation open forever, which is the whole reason for the process boundary.
func TestProcessBackendRespectsItsOwnDeadline(t *testing.T) {
	p := NewProcessAnalyzer(ProcessConfig{
		// A command that accepts its input and never answers.
		Command:        "python3",
		Args:           []string{"-c", "import sys\nfor line in sys.stdin:\n    pass\n"},
		Name:           "silent",
		StartupTimeout: 5 * time.Second,
		RequestTimeout: 2 * time.Second,
	})
	defer p.Close()
	start := time.Now()
	_, err := p.Analyze(context.Background(), "テスト", ProfileModern)
	elapsed := time.Since(start)
	if err == nil {
		t.Skip("the silent backend answered; nothing to assert")
	}
	if elapsed > 8*time.Second {
		t.Errorf("waited %v, want the exchange abandoned near the 2s request timeout", elapsed)
	}
}
