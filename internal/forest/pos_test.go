package forest

import "testing"

// 代名詞 was missing from the tag table, so every Japanese pronoun arrived with
// no part of speech at all. A token with no POS cannot head a noun phrase, so
// 「誰が云ふ。」 had nothing for が to attach to and the event ended with zero
// arguments — reported three stages later as no_arguments_bound, which points
// at the argument binder rather than at the tag table that had lost the word.
func TestPronounsGetAPartOfSpeech(t *testing.T) {
	for _, tag := range []string{"代名詞", "代名詞-一般", "代名詞-指示詞"} {
		p, ok := POSByTag(tag)
		if !ok || p != POSPronoun {
			t.Fatalf("POSByTag(%q) = %q,%v; want %q,true", tag, p, ok, POSPronoun)
		}
	}
}

// Only the first tag used to be consulted. A word analysed as 名詞,代名詞 or
// 動詞,非自立可能 then produced an untagged token whenever the leading tag was
// unknown, and the symptom was a missing argument rather than a missing tag.
func TestPOSFromTagsScansTheWholeTuple(t *testing.T) {
	for _, tc := range []struct {
		tags []string
		want POS
	}{
		{[]string{"名詞", "固有名詞", "一般"}, POSNoun},
		{[]string{"動詞", "非自立可能"}, POSVerb},
		{[]string{"名詞", "代名詞", "一般"}, POSNoun},
		{[]string{"助詞", "格助詞"}, POSParticle},
		{[]string{"形容詞"}, POSAdj},
		{[]string{"記号", "句点"}, POSPunct},
	} {
		if got := POSFromTags(tc.tags); got != tc.want {
			t.Fatalf("POSFromTags(%v) = %q; want %q", tc.tags, got, tc.want)
		}
	}
}

// A tag nothing knows must stay unknown rather than borrow a neighbouring
// reading. Guesswork here would put a word in the wrong role silently.
func TestPOSFromTagsUnknownStaysUnknown(t *testing.T) {
	if got := POSFromTags([]string{"ここだけの記号", "未知"}); got != "" {
		t.Fatalf("POSFromTags on unknown tags = %q; want empty", got)
	}
}

// The builtin backend already speaks this vocabulary, so it must round-trip.
func TestInternalPOSRoundTrips(t *testing.T) {
	for _, p := range []POS{POSNoun, POSProper, POSPronoun, POSVerb, POSAux,
		POSAdj, POSAdv, POSParticle, POSPunct, POSNum, POSInterj, POSAffix} {
		if got := POSFromTags([]string{string(p)}); got != p {
			t.Fatalf("POSFromTags(%q) = %q; want %q", p, got, p)
		}
	}
}
