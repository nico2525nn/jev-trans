package pipeline_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/lexicon"
	"github.com/nico/jev-trans/internal/pipeline"
)

// 現れる is the right subject for this test: the morphological analyser knows
// the word, so a clause and a head exist, but no curated table gives it a
// predicate. That gap between the two tables is exactly what a LexicalProvider
// exists to close, and it is the state the corpus diagnostic reported for 35
// distinct lexemes.
const providerSrc = "妖精が現れる。"

// An external inventory must actually reach the analysis, not merely exist as a
// type. Only an end-to-end test catches a chain that is constructed and then
// never consulted, which is how the first version of this went unnoticed: the
// type was there, the tests passed, and the corpus numbers did not move.
func TestExternalProviderReachesThePipeline(t *testing.T) {
	// The builtin analyser is pinned because the surface a lookup sees depends
	// on the backend, and a test that only holds for one of them would be a
	// claim about the environment rather than about the code.
	t.Setenv("JEV_SUDACHI_ADAPTER", "off")
	t.Setenv("JEV_LEXICON_TSV", "")

	resp := translateDoc(t, providerSrc, "without-inventory")
	if len(resp.Metrics.PredicateGaps) != 1 {
		t.Fatalf("expected 現れる to be unresolved without an inventory, got %+v",
			resp.Metrics.PredicateGaps)
	}

	path := writeInventory(t, "現れる\tMOVE.02:0.9\n現れる\tMOVE.02:0.9\n")
	t.Setenv("JEV_LEXICON_TSV", path)

	resp = translateDoc(t, providerSrc, "with-inventory")
	if len(resp.Metrics.PredicateGaps) != 0 {
		t.Fatalf("the inventory must resolve 現れる, got %+v", resp.Metrics.PredicateGaps)
	}
	if resp.Metrics.PredicatesResolved != resp.Metrics.PredicatesTotal {
		t.Fatalf("the predicate must be resolved, got %d/%d",
			resp.Metrics.PredicatesResolved, resp.Metrics.PredicatesTotal)
	}
}

// A row naming a sense the ontology does not define must be discarded, not
// quietly accepted. The ontology is closed, which is the same rule the verifier
// applies to a target feature with no provenance.
func TestProviderCannotIntroduceSenses(t *testing.T) {
	t.Setenv("JEV_SUDACHI_ADAPTER", "off")
	t.Setenv("JEV_LEXICON_TSV", writeInventory(t, "現れる\tNOT.A.SENSE:0.9\n"))

	resp := translateDoc(t, providerSrc, "closed-ontology")
	if resp.Metrics.PredicatesResolved == resp.Metrics.PredicatesTotal {
		t.Fatal("a sense outside the ontology must not resolve")
	}
}

// An inventory that is not there must not be an error. The chain falls through
// to the curated table and the corpus numbers stay what they are, which is the
// difference between "no dictionary is installed" and "the system does not
// know the word".
func TestAbsentInventoryIsNotAnError(t *testing.T) {
	t.Setenv("JEV_SUDACHI_ADAPTER", "off")
	t.Setenv("JEV_LEXICON_TSV", filepath.Join(t.TempDir(), "absent.tsv"))

	resp := translateDoc(t, providerSrc, "absent-inventory")
	if len(resp.Metrics.PredicateGaps) != 1 {
		t.Fatalf("an absent inventory must leave the gap as it was, got %+v",
			resp.Metrics.PredicateGaps)
	}
}

func writeInventory(t *testing.T, rows string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "extra.tsv")
	body := "# 検証用の述語辞書\n" + rows
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// translateDoc gives each call its own document id, because the engine caches
// by document and a second call under the same id returns the first answer.
func translateDoc(t *testing.T, text, doc string) *pipeline.Response {
	t.Helper()
	resp, err := pipeline.NewEngine(pipeline.EngineConfig{DefaultMode: "interactive"}).
		Translate(t.Context(), pipeline.Request{
			Text:       text,
			SourceLang: lang.JA,
			TargetLang: lang.EN,
			DocumentID: doc,
			Mode:       "full",
		})
	if err != nil {
		t.Fatalf("Translate(%q) returned %v", text, err)
	}
	return resp
}

// The chain is configuration, not an environment read. A process that sets
// JEV_LEXICON_TSV must not change the behaviour of an engine that was built
// with its own chain, or the answer depends on a moment the caller cannot see
// or record — and a verification run would not be reproducible.
func TestConfiguredChainWinsOverTheEnvironment(t *testing.T) {
	t.Setenv("JEV_SUDACHI_ADAPTER", "off")
	path := writeInventory(t, "現れる\tMOVE.02:0.9\n現れる\tMOVE.02:0.9\n")

	// An engine with no chain of its own inherits the environment, which is the
	// documented default and the state of a process configured only by env vars.
	t.Setenv("JEV_LEXICON_TSV", path)
	fromEnv, err := pipeline.NewEngine(pipeline.EngineConfig{DefaultMode: "interactive"}).
		Translate(t.Context(), pipeline.Request{
			Text: providerSrc, SourceLang: lang.JA, TargetLang: lang.EN,
			DocumentID: "env-chain", Mode: "full",
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(fromEnv.Metrics.PredicateGaps) != 0 {
		t.Fatalf("the environment chain must apply when none is configured: %+v",
			fromEnv.Metrics.PredicateGaps)
	}

	// An engine given the curated table alone must ignore the environment
	// entirely and leave the predicate open.
	curated := lexicon.NewSet(lexicon.NewTableProvider(lexicon.Default()))
	explicit, err := pipeline.NewEngine(pipeline.EngineConfig{
		DefaultMode: "interactive", Predicates: curated,
	}).Translate(t.Context(), pipeline.Request{
		Text: providerSrc, SourceLang: lang.JA, TargetLang: lang.EN,
		DocumentID: "explicit-chain", Mode: "full",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(explicit.Metrics.PredicateGaps) != 1 {
		t.Fatalf("an explicitly configured chain must override the environment, got %+v",
			explicit.Metrics.PredicateGaps)
	}
}
