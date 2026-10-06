package lexicon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetPrefersTheEarlierProvider(t *testing.T) {
	first := stubProvider{name: "first", ready: true, jp: map[string][]SenseHit{
		"する": {{SenseID: "DO.01", Weight: 1}},
	}}
	second := stubProvider{name: "second", ready: true, jp: map[string][]SenseHit{
		"する":  {{SenseID: "WRONG.01", Weight: 1}},
		"落ちる": {{SenseID: "FALL.02", Weight: 1}},
	}}
	s := NewSet(first, second)

	got := s.SensesJP("する")
	if len(got) != 1 || got[0].SenseID != "DO.01" {
		t.Fatalf("curated table must win over the external inventory, got %+v", got)
	}
	// A form the curated table is silent on still gets answered.
	got = s.SensesJP("落ちる")
	if len(got) != 1 || got[0].SenseID != "FALL.02" {
		t.Fatalf("chain must fall through to the external inventory, got %+v", got)
	}
	if got := s.SensesJP("未知"); got != nil {
		t.Fatalf("an unknown form must stay unknown, got %+v", got)
	}
}

func TestUnreadyProviderIsSkipped(t *testing.T) {
	// A provider whose data is absent reports itself unready, so the chain
	// falls through instead of the system reporting "no such word" for a
	// missing file.
	s := NewSet(stubProvider{name: "absent", ready: false}, NewTableProvider(Default()))
	if !s.Ready() {
		t.Fatal("the chain is ready because the curated table is")
	}
	if len(s.SensesJP("渡す")) == 0 {
		t.Fatal("a verb the curated table knows must still resolve")
	}
}

func TestFileProviderRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "extra.tsv")
	body := "# 追加の述語辞書\n" +
		"落ちる\tFALL.02:0.90\n" +
		"きらめき\tSHINE.03:0.80\n" +
		"fall\tDESCEND.01:0.70\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewFileProvider(path, "tsv")
	if !p.Ready() {
		t.Fatal("a readable file must be ready")
	}
	if hits := p.SensesJP("落ちる"); len(hits) != 1 || hits[0].SenseID != "FALL.02" {
		t.Fatalf("JP row not loaded: %+v", hits)
	}
	if hits := p.SensesEN("fall"); len(hits) != 1 || hits[0].SenseID != "DESCEND.01" {
		t.Fatalf("EN row not loaded: %+v", hits)
	}
	if hits := p.SensesJP("無い"); hits != nil {
		t.Fatalf("absent form must not resolve: %+v", hits)
	}
}

func TestFileProviderAbsentIsNotReady(t *testing.T) {
	p := NewFileProvider(filepath.Join(t.TempDir(), "nope.tsv"), "tsv")
	if p.Ready() {
		t.Fatal("a missing file must report itself unready")
	}
	if hits := p.SensesJP("落下"); hits != nil {
		t.Fatalf("an unready provider must answer nothing: %+v", hits)
	}
	if s := p.Stats(); s != nil {
		t.Fatalf("an unready provider must report no stats: %+v", s)
	}
}

func TestFileProviderIgnoresComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "extra.tsv")
	if err := os.WriteFile(path, []byte("# only a comment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if NewFileProvider(path, "tsv").Ready() {
		t.Fatal("a file with no rows holds no knowledge")
	}
}

type stubProvider struct {
	name  string
	jp    map[string][]SenseHit
	en    map[string][]SenseHit
	ready bool
}

func (s stubProvider) Name() string { return s.name }
func (s stubProvider) Ready() bool  { return s.ready }
func (s stubProvider) SensesJP(surface string) []SenseHit {
	return s.jp[surface]
}
func (s stubProvider) SensesEN(surface string) []SenseHit { return s.en[surface] }
func (s stubProvider) Stats() map[string]int              { return nil }
