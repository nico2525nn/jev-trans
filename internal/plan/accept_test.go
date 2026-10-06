package plan

import (
	"testing"

	"github.com/nico2525nn/jev-trans/internal/jlir"
)

func TestAcceptanceJapanese(t *testing.T) {
	// C.NAMED.JA.03 is 「〜という名前だ」: the frame names an entity, not an
	// event, so it is checked on its own terms.
	named := jaOut(t, EventPlan{
		EventID: "e1", SenseID: "COMMUNICATE.09", SenseFamily: "COMMUNICATE",
		Tense: jlir.TensePresent, Polarity: jlir.PolarityPositive,
		Construction: "C.NAMED.JA.03", Constructions: []string{"C.NAMED.JA.03"},
		Args: map[string]*NPPlan{jlir.RoleAgent: {Noun: "太郎", Proper: true},
			jlir.RolePatient: {Noun: "本"}},
	})
	if len(named) == 0 || named[0] != "太郎が本という名前だ。" {
		t.Errorf("C.NAMED.JA.03 wanted \"太郎が本という名前だ。\", got %v", named)
	}

	for _, tc := range []struct{ id, want string }{
		{"C.MUST.JA.01", "太郎が本を読まなければならない。"},
		{"C.SHOULD.JA.01", "太郎が米を食べるべきだ。"},
		{"C.INTEND.JA.01", "太郎が本を読むつもりだ。"},

		{"C.MAY.JA.01", "太郎が米を食べてもいい。"},
		{"C.WANT.JA.03", "太郎が本を読みたい。"},
		{"C.ABLE.JA.01", "太郎が本を読める。"},
	} {
		got := jaOut(t, EventPlan{
			EventID: "e1", SenseID: firstSense2(t, tc.id), SenseFamily: "",
			Tense: jlir.TensePresent, Polarity: jlir.PolarityPositive,
			Construction: tc.id, Constructions: []string{tc.id},
			Args: map[string]*NPPlan{jlir.RoleAgent: {Noun: "太郎", Proper: true},
				jlir.RoleTheme: {IsClause: true, Clause: readFor(tc.id)}},
		})
		ok := false
		for _, s := range got {
			if s == tc.want {
				ok = true
			}
		}
		if !ok {
			t.Errorf("%s wanted %q, got %v", tc.id, tc.want, got)
		}
	}
}

func firstSense2(t *testing.T, id string) string {
	c, ok := Default().Get(id)
	if !ok {
		t.Fatal(id)
	}
	return c.Senses[0]
}

// readFor names the verb a frame's ending continues. A frame is only
// grammatical over the verb it names: 〜べきだ attaches to 食べる, not to
// 読む, and the library says so in the construction.
func readFor(id string) *EventPlan {
	switch id {
	case "C.SHOULD.JA.01", "C.MAY.JA.01":
		return &EventPlan{
			EventID: "e3", SenseID: "CONSUME.01", SenseFamily: "CONSUME",
			Tense: jlir.TensePresent, Polarity: jlir.PolarityPositive,
			Construction: "C.EAT.JA.01", Constructions: []string{"C.EAT.JA.01"},
			Args: map[string]*NPPlan{jlir.RoleAgent: {Noun: "太郎", Proper: true},
				jlir.RolePatient: {Noun: "米"}},
		}
	}
	return &EventPlan{
		EventID: "e3", SenseID: "PERCEIVE.01", SenseFamily: "PERCEIVE",
		Tense: jlir.TensePresent, Polarity: jlir.PolarityPositive,
		Construction: "C.READ.JA.01", Constructions: []string{"C.READ.JA.01"},
		Args: map[string]*NPPlan{jlir.RoleAgent: {Noun: "太郎", Proper: true},
			jlir.RolePatient: {Noun: "本"}},
	}
}
