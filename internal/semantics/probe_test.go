package semantics

import (
	"testing"

	"github.com/nico/jev-trans/internal/lang"
	"github.com/nico/jev-trans/internal/lex"
	"github.com/nico/jev-trans/internal/syntax"
)

func TestProbe(t *testing.T) {
	src := "今日はいい天気ですね。"
	g := Analyze(src, lang.JA)
	for _, ev := range g.Events {
		t.Logf("Analyze: predicate=%q", ev.Predicate)
	}
	mf := lex.AnalyzeJA(src)
	b := syntax.ParseJA(src, mf)
	f := BuildForest(b, lang.JA)
	t.Logf("forest readings: %d", len(f.Readings))
	for i, r := range f.Readings {
		pred := ""
		if len(r.Graph.Events) > 0 {
			pred = r.Graph.Events[0].Predicate
		}
		t.Logf("  reading %d weight=%.3f origin=%q predicate=%q", i, r.Weight, r.Origin, pred)
	}
}
