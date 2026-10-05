package plan

// realize_jp.go: the Japanese morphological realizer.
//
// Japanese morphology is decided, not guessed: the verb class (godan row,
// ichidan, する, くる, copula, i-adjective, na-adjective) drives every
// inflection, so 読んでしまう, 読まない, 読まなかった, 読んでいます and
// 読まなければならない all come out of one class tag rather than a table of
// whole phrases.
//
// Two Japanese specific obligations are honoured here:
//
//   - politeness morphology (です/ます against だ/である) follows the style
//     profile, and the honorific form of a verb follows the honorific decision
//   - a zero argument is realized as an *absence*: the phrase contributes no
//     node at all, because surface preservation and meaning preservation are
//     different things (plan.md §3)

import (
	"strings"

	"github.com/nico/jev-trans/internal/forest"
	"github.com/nico/jev-trans/internal/jlir"
)

// jpVerb is a Japanese predicate with the conjugation class the inflections
// need. Class values are described in the row tables below.
//
// There is deliberately no honorific field. Honorificity is a property of the
// construction, not a form derivable from a plain verb: every honorific Japanese
// predicate is its own lexeme (お渡しする, 申す, 参见 the honorific stems), and
// appending ます to a dictionary form yields a non-word, because those stems
// already end in the dictionary る (おっしゃる, いらっしゃる, ご覧になる).
// Carrying both mechanisms produced 「という名前だおっしゃるます」.
type jpVerb struct {
	dic string // dictionary form
	cls string // conjugation class
}

// Japanese conjugation classes.
const (
	jpIchidan = "i"    // 食べる, 見る, いる
	jpU       = "u"    // 買う, 言う, 行く
	jpKu      = "ku"   // 書く, 聞く
	jpGu      = "gu"   // 泳ぐ
	jpSu      = "su"   // 出す, 話す-like godan verbs
	jpTsu     = "tsu"  // 立つ, 待つ, 持つ
	jpNu      = "nu"   // 死ぬ
	jpBu      = "bu"   // 飛ぶ, 遊ぶ
	jpMu      = "mu"   // 読む, 飲む
	jpRu      = "ru"   // 作る, 取る, 帰る, 言う-like
	jpSuru    = "s"    // する
	jpKuru    = "k"    // 来る
	jpCopula  = "da"   // だ, である
	jpIAdj    = "adj"  // いい, 欲しい, 大きい
	jpNaAdj   = "na"   // 好き, 静か, same
	jpNaru    = "naru" // になる compounds: ご覧になる, ご存じになる
	jpNominal = "nom"  // べき, こと
)

// jpNaruStems lists the honorific 〜になる compounds. Their stem is the honorific
// form itself (ご覧に, ご存じに), not a plain verb stem plus な, because the
// honorific stem already carries the に.
var jpNaruStems = map[string]string{
	"ご覧になる":  "ご覧に",
	"ご存じになる": "ご存じに",
}

// jpNegVowel is the godan negative row: う→わ, く→か, ぐ→が, す→さ, つ→た,
// ぬ→な, ぶ→ば, む→ま, る→ら.
var jpNegVowel = map[string]string{
	jpU: "わ", jpKu: "か", jpGu: "が", jpSu: "さ",
	jpTsu: "た", jpNu: "な", jpBu: "ば", jpMu: "ま", jpRu: "ら",
}

// jpTeRow is the godan て-form row change.
var jpTeRow = map[string]string{
	jpU: "って", jpKu: "いて", jpGu: "いで", jpSu: "して", jpTsu: "って",
	jpNu: "んで", jpBu: "んで", jpMu: "んで", jpRu: "って",
}

// jpRenyouRow is the godan 連用形 row as it appears before たい: 買いたい,
// 書きたい, 泳ぎたい, 話したい, 待ちたい, 死にたい, 飛みたい, 飲みたい,
// 作りたい, 帰りたい. It agrees with jpPoliteConsonant everywhere except く
// and つ, which are irregular here (書く→書きたい, not 書きたい; 待つ→待ちたい,
// not ちたい), so it is its own table rather than a reuse of that one.
var jpRenyouRow = map[string]string{
	jpSu: "し", jpU: "い", jpKu: "い", jpGu: "ぎ", jpTsu: "っ",
	jpNu: "に", jpBu: "び", jpMu: "み", jpRu: "り",
}

// jpPotentialRow is the godan potential row change.
var jpPotentialRow = map[string]string{
	jpU: "える", jpKu: "ける", jpGu: "げる", jpSu: "せる", jpTsu: "てる",
	jpNu: "ねる", jpBu: "べる", jpMu: "める", jpRu: "れる",
}

// jpPoliteConsonant is the consonant the polite suffix takes from the godan
// row: 読みます, 書きます, 泳ぎます, 死にます, 飲みます, 作ります. It is not
// derivable from the negative row vowel, so it is spelled out.
var jpPoliteConsonant = map[string]string{
	jpU: "い", jpKu: "き", jpGu: "ぎ", jpSu: "し", jpTsu: "ち",
	jpNu: "に", jpBu: "び", jpMu: "み", jpRu: "り",
}

// jpPastRow is the godan past row change, which is not derivable from the
// negative stem: 渡さなかった is negative, 渡した is past.
var jpPastRow = map[string]string{
	jpU: "っ", jpKu: "い", jpGu: "い", jpSu: "し", jpTsu: "っ",
	jpNu: "ん", jpBu: "ん", jpMu: "ん", jpRu: "っ",
}

// jpFamilyVerb is the Japanese verb lexicon, keyed by construction family. The
// constructions reference it; a construction with its own Lex overrides it.
var jpFamilyVerb = map[string]jpVerb{
	"GIVE":    {dic: "渡す", cls: jpRu},
	"RECEIVE": {dic: "受け取る", cls: jpRu},
	"TAKE":    {dic: "取る", cls: jpRu},
	"SAY":     {dic: "言う", cls: jpU},
	"TELL":    {dic: "伝える", cls: jpRu},
	"ASK":     {dic: "尋ねる", cls: jpRu},
	"ANSWER":  {dic: "答える", cls: jpRu},
	"SPEAK":   {dic: "話す", cls: jpSu},
	"MOVE":    {dic: "行く", cls: jpU},
	"ARRIVE":  {dic: "着く", cls: jpKu},
	"LEAVE":   {dic: "出る", cls: jpRu},
	"MEET":    {dic: "会う", cls: jpU},
	"WANT":    {dic: "欲しい", cls: jpIAdj},
	"INTEND":  {dic: "思う", cls: jpU},
	"BE":      {dic: "いる", cls: jpRu},
	"EXIST":   {dic: "ある", cls: jpRu},
	"NAMED":   {dic: "言う", cls: jpU},
	"BECOME":  {dic: "なる", cls: jpRu},
	"HAVE":    {dic: "持つ", cls: jpTsu},
	"ABLE":    {dic: "出来る", cls: jpIchidan},
	"MUST":    {dic: "", cls: jpNominal},
	"SHOULD":  {dic: "", cls: jpNominal},
	"MAY":     {dic: "いい", cls: jpIAdj},
	"EAT":     {dic: "食べる", cls: jpIchidan},
	"DRINK":   {dic: "飲む", cls: jpMu},
	"SEE":     {dic: "見る", cls: jpIchidan},
	"HEAR":    {dic: "聞く", cls: jpKu},
	"KNOW":    {dic: "知る", cls: jpRu},
	"THINK":   {dic: "思う", cls: jpU},
	"BELIEVE": {dic: "思う", cls: jpU},
	"COMPARE": {dic: "比べる", cls: jpRu},
	"COPULA":  {dic: "だ", cls: jpCopula},
	"LIKE":    {dic: "好き", cls: jpNaAdj},
	"SHOW":    {dic: "見せる", cls: jpRu},
	"READ":    {dic: "読む", cls: jpMu},
	"WRITE":   {dic: "書く", cls: jpKu},
	"SLEEP":   {dic: "寝る", cls: jpRu},
	"LIVE":    {dic: "住む", cls: jpMu},
	"WORK":    {dic: "働く", cls: jpKu},
	"OPEN":    {dic: "開く", cls: jpKu},
	"CLOSE":   {dic: "閉める", cls: jpRu},
	"START":   {dic: "始める", cls: jpRu},
	"STOP":    {dic: "やめる", cls: jpRu},
	"SEND":    {dic: "送る", cls: jpRu},
	"BUY":     {dic: "買う", cls: jpU},
	"SELL":    {dic: "売る", cls: jpRu},
}

// taiStem is the stem 〜たい attaches to. For an ichidan verb it is the stem
// itself (食べたい); for a godan verb it is the stem plus the 連用形 row
// consonant (飲みたい, 待ちたい), which is neither the te-form nor the masu stem.
func (v jpVerb) taiStem() string {
	switch v.cls {
	case jpIchidan, jpIAdj, jpNaAdj, jpSuru, jpKuru, jpNaru:
		return v.stem()
	}
	if r, ok := jpRenyouRow[v.cls]; ok {
		return v.stem() + r
	}
	return v.te()
}

// taStem is the stem 〜てもいい and 〜てはいけない attach to: the te-form minus
// the て that closes it. For the n-row verbs that て is で, not て — 読んで ends
// in で, so trimming a literal て gave 「読んでてもいい」 instead of
// 「読んでもいい」.
func (v jpVerb) taStem() string {
	t := v.te()
	switch {
	case strings.HasSuffix(t, "で"):
		return t[:len(t)-len("で")]
	case strings.HasSuffix(t, "て"):
		return t[:len(t)-len("て")]
	}
	return t
}

// stem returns the 連用形 (masu stem) of a verb: the form polite ます attaches
// to, and the base of the ichidan te-form.
func (v jpVerb) stem() string {
	switch v.cls {
	case jpNaru:
		// A になる compound conjugates from the stem of になる itself, which is
		// な: ご覧になる is ご覧に+なる, so its stem is ご覧に+な. The shorter
		// ご覧に gave ご覧にります.
		if st, ok := jpNaruStems[v.dic]; ok {
			return st + "な"
		}
		return v.dic + "な"
	case jpSuru:
		if v.dic == "する" {
			return "し"
		}
	case jpKuru:
		return "き"
	case jpCopula, jpNominal, jpIAdj, jpNaAdj:
		return v.dic
	}
	// A する compound keeps its full verb: お渡しする is a verb stem お渡し plus
	// する, so the stem drops both morphemes.
	if strings.HasSuffix(v.dic, "する") {
		return dropLastRune(dropLastRune(v.dic))
	}
	// Every other dictionary form ends in one inflectional mora: る for the
	// ichidan and る-verbs, す for the す-verbs (渡す), う for the う-verbs.
	return dropLastRune(v.dic)
}

// dropLastRune removes the final rune of a UTF-8 string.
func dropLastRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return ""
	}
	return string(r[:len(r)-1])
}

// negStem returns the stem the plain negative attaches to.
func (v jpVerb) negStem() string {
	switch v.cls {
	case jpNaru:
		return v.stem()
	case jpIchidan, jpIAdj:
		return v.stem()
	case jpSuru:
		return "し"
	case jpKuru:
		return "こ"
	case jpCopula:
		return ""
	case jpNaAdj, jpNominal:
		return v.dic
	}
	if r, ok := jpNegVowel[v.cls]; ok {
		return v.stem() + r
	}
	return v.stem()
}

// te returns the て-form.
func (v jpVerb) te() string {
	switch v.cls {
	case jpNaru:
		// ご覧になって: the stem already ends in に, so the て-form is a って
		// attachment rather than a row change.
		return v.stem() + "って"
	case jpIchidan:
		return v.stem()
	case jpSuru:
		return "し"
	case jpKuru:
		return "き"
	case jpIAdj, jpNaAdj, jpNominal, jpCopula:
		return ""
	}
	if r, ok := jpTeRow[v.cls]; ok {
		return v.stem() + r
	}
	return v.stem()
}

// potential returns the potential form.
func (v jpVerb) potential() string {
	switch v.cls {
	case jpNaru:
		return v.stem() + "れる"
	case jpIchidan:
		return v.stem() + "られる"
	case jpSuru:
		return "できる"
	case jpKuru:
		return "こられる"
	case jpIAdj, jpNaAdj, jpNominal, jpCopula:
		return ""
	}
	if r, ok := jpPotentialRow[v.cls]; ok {
		// The potential of a godan verb attaches the row to the STEM
		// (読める), not to the negative stem, which gave 読まえる.
		return v.stem() + r
	}
	return ""
}

// negative returns the plain negative form.
func (v jpVerb) negative() string {
	switch v.cls {
	case jpSuru:
		return "しない"
	case jpKuru:
		return "こない"
	case jpIAdj:
		return v.dic + "くない"
	case jpNaru:
		return v.stem() + "らない"
	case jpCopula:
		return "ではない"
	case jpNaAdj, jpNominal:
		return v.dic + "ではない"
	}
	if v.dic == "" {
		return ""
	}
	return v.negStem() + "ない"
}

// negativePolite returns the polite negative form.
func (v jpVerb) negativePolite() string {
	switch v.cls {
	case jpCopula:
		return "ではありません"
	case jpNaru:
		return v.stem() + "りません"
	case jpNaAdj, jpNominal, jpIAdj:
		if v.dic == "" {
			return ""
		}
		return v.dic + "ではない"
	}
	if v.dic == "" {
		return ""
	}
	return v.politeStem() + "ません"
}

// past returns the plain past form.
func (v jpVerb) past() string {
	switch v.cls {
	case jpSuru:
		return "しなかった"
	case jpKuru:
		return "こなかった"
	case jpNaru:
		// ご覧になった: a になる compound is な in its past too.
		return v.stem() + "った"
	case jpIchidan:
		return v.stem() + "なかった"
	case jpIAdj:
		return v.dic + "かった"
	case jpCopula:
		return "だった"
	case jpNaAdj, jpNominal:
		return v.dic + "ではなかった"
	}
	if r, ok := jpPastRow[v.cls]; ok {
		stem := v.stem()
		// The geminate appears only after a consonant final 連用形: 買った,
		// but あげた, whose 連用形 ends in a vowel.
		if r == "っ" && endsWithVowelKana(stem) {
			r = ""
		}
		return stem + r + "た"
	}
	return v.negStem() + "かった"
}

// endsWithVowelKana reports whether a stem ends in a vowel mora, which is what
// separates あげた from 買った.
func endsWithVowelKana(stem string) bool {
	// A voiced final mora (あげ, 買い,  swam) ends in a vowel too, so the
	// dakuten is stripped before the class is decided.
	stem = strings.TrimRight(stem, "\u3099\u309A")
	runes := []rune(stem)
	if len(runes) == 0 {
		return false
	}
	switch runes[len(runes)-1] {
	case 'あ', 'い', 'う', 'え', 'お', 'か', 'き', 'く', 'け', 'こ',
		'さ', 'し', 'す', 'せ', 'そ', 'た', 'ち', 'つ', 'て', 'と',
		'な', 'に', 'ぬ', 'ね', 'の', 'は', 'ひ', 'ふ', 'へ', 'ほ',
		'ま', 'み', 'む', 'め', 'も', 'や', 'ゆ', 'よ', 'ら', 'り',
		'る', 'れ', 'ろ', 'わ', 'ゐ', 'ゑ', 'を', 'ん',
		// voiced and semi-voiced kana are vowels too (あげ,  accusative forms)
		'が', 'ぎ', 'ぐ', 'げ', 'ご', 'ざ', 'じ', 'ず', 'ぜ', 'ぞ',
		'だ', 'ぢ', 'づ', 'で', 'ど', 'ば', 'び', 'ぶ', 'べ', 'ぼ',
		'ぱ', 'ぴ', 'ぷ', 'ぺ', 'ぽ', 'ゃ', 'ゅ', 'ょ', 'っ':
		return true
	}
	return false
}

// politeStem is the form the polite suffix ます attaches to. For a godan verb
// it carries the row consonant (読みます, 書きます), for する compounds the し of
// します (お渡しします).
func (v jpVerb) politeStem() string {
	switch v.cls {
	case jpSuru:
		if v.dic == "する" {
			return "し"
		}
		return v.stem() + "し"
	case jpKuru:
		return "き"
	case jpNaru:
		return v.stem()
	case jpIchidan, jpIAdj, jpNaAdj, jpNominal, jpCopula:
		return v.stem()
	}
	if c, ok := jpPoliteConsonant[v.cls]; ok {
		return v.stem() + c
	}
	return v.stem()
}

// polite returns the polite ます form.
func (v jpVerb) polite() string {
	switch v.cls {
	case jpCopula:
		return "です"
	case jpNaru:
		return v.stem() + "ります"
	case jpIAdj, jpNaAdj, jpNominal:
		if v.dic == "" {
			return ""
		}
		return v.dic + "です"
	}
	if v.dic == "" {
		return ""
	}
	return v.politeStem() + "ます"
}

// pastPolite returns the polite past form.
func (v jpVerb) pastPolite() string {
	switch v.cls {
	case jpCopula:
		return "でした"
	case jpNaru:
		return v.stem() + "りました"
	case jpIAdj, jpNaAdj, jpNominal:
		if v.dic == "" {
			return ""
		}
		return v.dic + "でした"
	}
	if v.dic == "" {
		return ""
	}
	return v.politeStem() + "ました"
}

// negativePastPolite returns the polite negative past form.
func (v jpVerb) negativePastPolite() string {
	switch v.cls {
	case jpCopula:
		return "ではありませんでした"
	case jpNaru:
		return v.stem() + "りませんでした"
	case jpIAdj, jpNaAdj, jpNominal:
		if v.dic == "" {
			return ""
		}
		return v.dic + "ではありませんでした"
	}
	if v.dic == "" {
		return ""
	}
	return v.politeStem() + "ませんでした"
}

// jpConjugate realizes a verb for an event plan: tense, polarity, politeness
// and aspect all come from the plan, never from the surface. quoted marks a
// reporting verb whose content is a quoted clause: it keeps the plain form,
// because the clause carries the tense.
func jpConjugate(v jpVerb, ep *EventPlan, quoted bool) string {
	polite := ep != nil && ep.Politeness >= 0.5
	negative := ep != nil && strings.EqualFold(ep.Polarity, jlir.PolarityNegative)
	past := ep != nil && ep.Tense == jlir.TensePast

	if v.cls == jpNominal && v.dic == "" {
		return ""
	}
	// Aspect and completion both build on the て-form.
	if ep != nil {
		te := v.te()
		switch {
		case strings.EqualFold(ep.Aspect, jlir.AspectProgressive) && te != "":
			if polite {
				return te + "ています"
			}
			return te + "ている"
		case strings.EqualFold(ep.Completion, jlir.CompletionCompleted) && te != "":
			if past {
				if polite {
					return te + "しまいました"
				}
				return te + "しまった"
			}
			if polite {
				return te + "てしまいます"
			}
			return te + "てしまう"
		}
	}
	switch {
	case negative && past && polite:
		return v.negativePastPolite()
	case negative && polite:
		return v.negativePolite()
	case negative:
		return v.negative()
	case past && polite:
		return v.pastPolite()
	case past:
		return v.past()
	case polite:
		return v.polite()
	}
	return v.dic
}

// --- the clause -----------------------------------------------------------

// framesJP materializes one event plan into one frame per surviving Japanese
// construction, appending the sentence final particle the projection chose.
func (rt *realizer) framesJP(ep *EventPlan, first bool, depth int) []frame {
	var out []frame
	for _, c := range rt.constructionsOf(ep) {
		if ok, rule, why := rt.checkConstruction(ep, c); !ok {
			rt.reject(string(ep.EventID), c.ID, rule, why, "realization")
			continue
		}
		usedV := false
		parts := rt.patternJP(ep, c, depth, &usedV)
		if len(parts) == 0 {
			continue // the frame declined itself (a clausal argument it cannot carry)
		}
		if rt.unextendable(parts) {
			rt.reject(string(ep.EventID), c.ID, HardMissingArg,
				"construction "+c.ID+" puts material after an embedded clause", "realization")
			continue
		}
		if sfp := rt.p.SentenceFinal; sfp != "" && usedV && rt.clauseFinal && !rt.embedded {
			parts = append(parts, piece{label: "SFP",
				alts: []forest.Alt{{Lex: sfp, Probability: 0.85, Hard: true}}})
		}
		parts = append(parts, rt.idPiece(c))
		if len(parts) <= 1 {
			continue
		}
		out = append(out, frame{c: c, parts: parts})
	}
	return out
}

// patternJP walks a Japanese construction pattern. A lowercase token is a case
// particle that marks the slot immediately to its LEFT, because Japanese writes
// the particle after the noun phrase it belongs to: "SUBJ OBJ を RECIPIENT に V".
// SUBJ is the one slot the pattern does not mark: は versus が is an
// information-structure decision of the projection (plan.md §19), so the
// pattern's SUBJ takes whatever subject marker the projection chose.
//
// The pattern is the owner of every other particle. Reading it as first
// precedence and the projection's NPPlan.Particle as a fallback is what produced
// 「〜を同じです」 and 「〜を〜を比べます」: the projection's を was consulted
// first, so the particle the pattern states about を or と was never emitted and
// a stray を was appended where none belonged.
//
// "quote" folds the clausal argument into the predicate, because Japanese puts
// 〜と before the verb, and the fold keeps the embedded clause's own
// alternatives in the forest instead of collapsing it to one string.
func (rt *realizer) patternJP(ep *EventPlan, c *Construction, depth int, usedV *bool) []piece {
	var parts []piece
	seenV := false
	add := func(p piece) {
		if p.empty() {
			return
		}
		parts = append(parts, p)
	}
	toks := tokens(c.Pattern)
	// marks[i] is the particle the pattern wrote after slot i, and
	// consumed[i] records that the token at i was such a particle rather than
	// an unplaced one.
	marks := make([]string, len(toks))
	consumed := make([]bool, len(toks))
	for i := range toks {
		if i+1 < len(toks) && isSlotToken(toks[i]) && !isSlotToken(toks[i+1]) {
			marks[i] = toks[i+1]
			consumed[i+1] = true
		}
	}
	for i, tok := range toks {
		if consumed[i] {
			continue
		}
		mark := marks[i]
		switch tok {
		case "SUBJ":
			if rt.noSubject {
				continue
			}
			np := resolveSubject(ep, c)
			subject := rt.jpNPPiece(np, "SUBJ", jpSubjectMark(np, c), false, depth, c)
			add(subject)
		case "V":
			if seenV {
				continue
			}
			alts := rt.jpVerbPiece(ep, c, depth)
			seenV = true
			*usedV = true
			if len(alts) > 0 {
				parts = append(parts, piece{label: "V", alts: alts})
			}
		case "JOIN":
			// Japanese complementation is carried by the quoting particle inside
			// the predicate, not by a joiner slot.
		case "TAIL":
			if c.Tail != "" {
				parts = append(parts, piece{label: "TAIL",
					alts: []forest.Alt{{Lex: c.Tail, Probability: 0.9, Hard: true}}})
			}
		case "quote":
			if np := complementArg(ep); np != nil && np.Clause != nil {
				sub := rt.quoteClause(ep, c, np, depth)
				if sub == "" {
					rt.reject(string(ep.EventID), c.ID, HardMissingArg,
						"construction "+c.ID+" cannot realize its quoted clause", "realization")
					return nil
				}
				add(piece{label: "V", sealed: true, alts: []forest.Alt{{
					Probability: c.Naturalness, Rule: "<" + c.ID + ">", Hard: true,
					Children: []string{sub},
				}}})
				seenV = true
				*usedV = true
			}
		case "OBJ":
			role := objectRole(ep)
			np := ep.Args[role]
			if np != nil && np.IsClause && c.Chain != "" {
				// A thematic-verb chain continues the clause's own predicate, so
				// the clause itself is not a subtree here — but its arguments
				// belong in the matrix frame, because Japanese writes them
				// there: 「太郎は本を読まなければならない」, not a nested clause.
				// The theme clause's own arguments are read from ITS plan, not
				// from the matrix event: the matrix event binds one role (the
				// theme) to the clause, and the clause is what names the object.
				subjNP := resolveSubject(ep, c)
				for _, r := range sortedRoles(np.Clause.Args) {
					arg := np.Clause.Args[r]
					if r == role || arg == nil || arg.IsClause || arg.Omitted() {
						continue
					}
					// The matrix subject already realizes the same referent when
					// the theme names it, so repeating it would give 「太郎が
					// 太郎を...」. The comparison is on the referent, not the
					// pointer: the two plans are separate objects.
					if isSubjectRole(r) && subjNP != nil && arg.EntityID == subjNP.EntityID {
						continue
					}
					// Each theme argument takes the particle the pattern writes
					// after OBJ; a theme with several arguments has one particle,
					// so the first gets it and the rest are bare.
					p := mark
					mark = ""
					add(rt.jpNPPiece(arg, roleOfToken(r), p, false, depth, c))
				}
				continue
			}
			if np != nil && np.IsClause {
				if c.Complement == "quot" {
					// Folded into the predicate by the "quote" token: the
					// clausal argument is the verb, not a phrase.
					continue
				}
				// Japanese puts 〜と before the verb, so a clausal argument can
				// only be realized by a quoting or a chain construction.
				rt.reject(string(ep.EventID), c.ID, HardMissingArg,
					"construction "+c.ID+" cannot realize a clausal argument in Japanese", "realization")
				return nil
			}
			add(rt.jpNPPiece(np, "OBJ", mark, false, depth, c))
		default:
			if isSlotToken(tok) {
				role := roleOfToken(tok)
				np := ep.Args[role]
				if np != nil && np.IsClause && (c.Complement == "quot" || c.Chain != "") {
					continue
				}
				add(rt.jpNPPiece(np, tok, mark, false, depth, c))
				continue
			}
			// A bare particle with no slot to its left is a data error in the
			// pattern, not a particle waiting for a slot.
			rt.note("construction %s: the particle %q has no slot to mark", c.ID, tok)
		}
	}
	return parts
}

// jpSubjectMark returns the topic/subject marker of a Japanese subject. は and
// が are chosen by the projection from the information structure (plan.md §19),
// so no construction states one.
func jpSubjectMark(np *NPPlan, c *Construction) string {
	if np == nil {
		return Ga
	}
	if np.Particle == Wa || np.Particle == Ga {
		return np.Particle
	}
	return Ga
}

func (rt *realizer) jpNPPiece(np *NPPlan, slot, particle string, initial bool, depth int, c *Construction) piece {
	if np == nil || np.Omitted() {
		return piece{label: slot}
	}
	if np.IsClause {
		if np.Clause == nil {
			return piece{label: slot}
		}
		return piece{label: slot, clause: np, depth: depth + 1}
	}
	return piece{label: slot, alts: rt.jpNPAlts(np, slot, particle, initial)}
}

// quoteClause realizes a quoted clause (〜と思う) as a subtree of the packed
// forest whose continuation is the reporting verb. The packed forest
// concatenates an alternative's lexeme BEFORE its child, so a clause followed by
// a verb cannot be two sibling pieces; it has to be one subtree. Building it
// that way, rather than realizing it into a throwaway forest and collapsing it to
// trees[0].Text(), is what keeps the embedded clause's own construction
// alternatives alive in the candidate set (plan.md §17, §38).
func (rt *realizer) quoteClause(ep *EventPlan, c *Construction, np *NPPlan, depth int) string {
	// The reporting verb is conjugated in its own right — 「と思った」 is past
	// because the reporting event is past, not because the quoted event is —
	// and it takes the quoting particle と before it.
	v := lookupJPVerb(c.Lex, c.Family)
	form := jpConjugate(v, ep, false)
	if form == "" {
		form = orDefault(v.dic, c.Lex)
	}
	if strings.EqualFold(ep.Polarity, jlir.PolarityNegative) &&
		!strings.Contains(form, "ない") && !strings.Contains(form, "ません") &&
		!strings.Contains(form, "では") {
		rt.reject(string(ep.EventID), form, HardPolarity,
			"negative polarity of "+string(ep.EventID)+" has no Japanese realization", "realization")
		return ""
	}
	// The continuation has to be an interned node: the chain builder splices it
	// in as a child id, and handing it a bare lexeme made the forest resolve it
	// as some unrelated node.
	return rt.clauseTail(np.Clause, rt.terminal("QTAIL", To+form), false, depth+1)
}

// jpVerbPiece builds the predicate of one Japanese clause: tense, polarity,
// politeness, aspect and completion all derive from the plan.
func (rt *realizer) jpVerbPiece(ep *EventPlan, c *Construction, depth int) []forest.Alt {
	if c.Chain != "" {
		return rt.jpChainPiece(ep, c)
	}
	v := lookupJPVerb(c.Lex, c.Family)
	form := jpConjugate(v, ep, false)
	if form == "" {
		rt.reject(string(ep.EventID), c.Lex, HardUnknownSense,
			"no Japanese form for "+c.Family, "realization")
		return nil
	}
	if strings.EqualFold(ep.Polarity, jlir.PolarityNegative) && !strings.Contains(form, "ない") &&
		!strings.Contains(form, "ません") && !strings.Contains(form, "では") {
		rt.reject(string(ep.EventID), form, HardPolarity,
			"negative polarity of "+string(ep.EventID)+" has no Japanese realization", "realization")
		return nil
	}
	return []forest.Alt{{Lex: form, Probability: c.Naturalness, Rule: "<" + c.ID + ">", Hard: true}}
}

// jpChainPiece builds a predicate that continues from the thematic verb, such
// as 食べなければならない or 食べたい. The thematic verb is the one the embedded
// clause carries; when there is no embedded clause the chain has nothing to
// continue from and that is reported rather than faked.
func (rt *realizer) jpChainPiece(ep *EventPlan, c *Construction) []forest.Alt {
	np := complementArg(ep)
	theme := jpThemeVerb(ep)
	var stem string
	switch c.Chain {
	case "nai":
		stem = theme.negStem()
	case "nai_n":
		stem = theme.negStem() + "な"
	case "te":
		stem = theme.te()
	case "ta":
		stem = theme.taStem()
	case "tai":
		stem = theme.taiStem()
	case "dic":
		stem = theme.dic
	case "potential":
		stem = theme.potential()
	}
	if theme.dic == "" {
		rt.note("chain construction %s has no thematic verb to continue from", c.ID)
		if np != nil && np.Noun != "" {
			rt.reject(string(ep.EventID), np.Noun, HardMissingArg,
				"chain construction "+c.ID+" needs a thematic verb, which the source does not supply", "realization")
		}
		stem = ""
	}
	return []forest.Alt{{Lex: stem, Probability: c.Naturalness, Rule: "<" + c.ID + ">", Hard: true}}
}

// jpThemeVerb returns the Japanese verb of the event the chain continues from.
func jpThemeVerb(ep *EventPlan) jpVerb {
	np := complementArg(ep)
	if np == nil || np.Clause == nil {
		return jpVerb{}
	}
	sub := np.Clause
	for _, id := range sub.Constructions {
		if c, ok := Default().Get(id); ok {
			return lookupJPVerb(c.Lex, c.Family)
		}
	}
	return jpVerb{}
}

// lookupJPVerb resolves the verb of a construction: its own Lex when it has
// one, the family lexicon otherwise. A literal surface is classified by its
// dictionary form so that a construction may name its verb directly.
func lookupJPVerb(lex, family string) jpVerb {
	if lex == "" {
		if v, ok := jpFamilyVerb[family]; ok {
			return v
		}
		return jpVerb{}
	}
	if v, ok := jpFamilyVerb[lex]; ok {
		return v
	}
	if cls, ok := jpVerbClassOf[lex]; ok {
		return jpVerb{dic: lex, cls: cls}
	}
	return jpVerb{dic: lex, cls: jpClassOf(lex)}
}

// jpVerbClassOf is the authoritative class table for the exact surfaces the
// constructions name. A class can never be guessed from the final kana alone
// (あげる is a う-verb while 取る is a る-verb), so every literal the library
// uses is registered here; jpClassOf is only the last resort.
var jpVerbClassOf = map[string]string{
	"渡す": jpSu, "あげる": jpU, "お渡しする": jpSuru, "上げる": jpU,
	"受け取る": jpRu, "もらう": jpU, "いただく": jpKu, "取る": jpRu,
	"言う": jpU, "申す": jpU, "おっしゃる": jpU, "伝える": jpRu,
	"尋ねる": jpRu, "聞く": jpKu, "頼む": jpU, "答える": jpU,
	"話す": jpSu, "行く": jpU, "着く": jpKu, "到着する": jpSuru,
	"出る": jpRu, "去る": jpRu, "会う": jpU, "お会いする": jpSuru,
	"欲しい": jpIAdj, "ほしい": jpIAdj, "たい": jpIAdj,
	"思う": jpU, "考える": jpRu, "打算する": jpSuru,
	"いる": jpRu, "いらっしゃる": jpU, "ある": jpRu, "なる": jpRu,
	"持つ": jpTsu, "お持ちする": jpSuru, "できる": jpIchidan, "出来る": jpIchidan,
	"いい": jpIAdj, "よい": jpIAdj, "食べる": jpIchidan, "観る": jpIchidan,
	"飲む": jpMu, "見る": jpIchidan, "聞こえる": jpIchidan,
	// ご覧になる / ご存じになる are the honorific 〜になる compounds: a verb
	// stem plus になる. Classifying them as i-adjectives made the polite form
	// append です to a whole phrase: 「本をご覧になるです」.
	"ご覧になる": jpNaru, "ご存じになる": jpNaru,
	"知る": jpRu, "比べる": jpRu, "似ている": jpIchidan,
	"同じ": jpIAdj, "好き": jpNaAdj, "嫌い": jpNaAdj, "見せる": jpRu,
	"読む": jpMu, "書く": jpKu, "寝る": jpRu, "住む": jpMu, "働く": jpKu,
	"開く": jpKu, "閉める": jpRu, "始める": jpRu, "やめる": jpRu,
	"送る": jpRu, "買う": jpU, "売る": jpRu, "お売りする": jpSuru,
	"使う": jpU, "立つ": jpTsu, "死ぬ": jpNu, "飛ぶ": jpBu, "作る": jpRu,
	"帰る": jpRu, "出す": jpSu, "泳ぐ": jpGu, "泣く": jpKu, "急ぐ": jpGu,
	"待つ": jpTsu, "遊ぶ": jpBu, "知る人": jpRu, "始まる": jpRu,
	// 読む and 知る's neighbours are not in the construction library, but the
	// chain frames build on whatever verb the theme carries, so the common
	// stems the analyzer produces are registered too. Without them 読む fell
	// through to jpClassOf, which classifies by final kana and called it a
	// う-verb: 読める, 買いたい, 帰りたい all came out wrong.
	"終わる": jpRu, "わかる": jpRu, "分かる": jpRu, "載る": jpRu,
	"おっしゃる人": jpU, "なさる": jpU, "なさる人": jpU,
	"だ": jpCopula, "である": jpCopula, "です": jpCopula,
}

// jpClassOf classifies a Japanese verb surface by its dictionary ending. This is
// the only morphological guess the realizer makes, and it is a guess about the
// *form*, never about the meaning: a misclassification produces a wrong ending,
// not a wrong translation.
func jpClassOf(lex string) string {
	if lex == "" {
		return jpNominal
	}
	switch lex {
	case "だ", "である":
		return jpCopula
	case "する":
		return jpSuru
	case "くる", "来る":
		return jpKuru
	}
	// A 〜になる compound behaves as なる: になる → になった, になって.
	if strings.HasSuffix(lex, "になる") {
		return jpNaru
	}
	// A verb whose dictionary form ends in the honorific 〜になる compound
	// pattern is one even when the ending is not literally になる.
	if _, ok := jpNaruStems[lex]; ok {
		return jpNaru
	}
	runes := []rune(lex)
	last := string(runes[len(runes)-1])
	switch last {
	case "いる":
		return jpIchidan
	case "う":
		return jpU
	case "く":
		return jpKu
	case "ぐ":
		return jpGu
	case "す":
		return jpSu
	case "つ":
		return jpTsu
	case "ぬ":
		return jpNu
	case "ぶ":
		return jpBu
	case "む":
		return jpMu
	case "る":
		return jpRu
	case "い":
		return jpIAdj
	}
	// Words ending in ない are already negative forms of an ichidan or godan
	// verb; classify them so that nothing doubles a ない.
	if strings.HasSuffix(lex, "ない") {
		return jpNominal
	}
	return jpNominal
}

// --- Japanese noun phrases ------------------------------------------------

// jpNPAlts builds the alternatives of one Japanese phrase.
func (rt *realizer) jpNPAlts(np *NPPlan, slot, particle string, initial bool) []forest.Alt {
	var alts []forest.Alt
	// The construction pattern owns the case particle. The projection's
	// NPPlan.Particle is used only where the pattern is silent (the subject,
	// where は/が is an information-structure decision), so re-adding it here as
	// a rival alternative is what produced 「〜を同じです」 and 「〜を〜を比べます」:
	// both appeared alongside the pattern's own と.
	particles := []string{particle}

	// Pronoun reading: suppressed unless the projection licensed it, because
	// plan.md §47 forbids turning every English "he" into 彼.
	pronouns := np.PronounOpts
	if np.Pronoun != "" && !containsString(pronouns, np.Pronoun) {
		pronouns = append([]string{np.Pronoun}, pronouns...)
	}
	if len(pronouns) > 0 && rt.r.Style.Normalized().PronounExplicitness >= 0.25 {
		for i, pron := range pronouns {
			if !rt.pronounAllowed(np, jpGenderOf(pron), slot) {
				continue
			}
			alts = append(alts, forest.Alt{
				Lex: pron + particle, Probability: pronProb(np, pron, i), Hard: true,
				Rule: "<pronoun>", Note: "pronoun realization",
			})
		}
	}

	noun := np.Noun
	if noun == "" {
		noun = aliasFor(rt.g, rt.entity(np), rt.l)
	}
	if noun == "" {
		// The graph knows the referent only in the source language; the
		// caller's resolver supplies the target surface, and declining means a
		// genuine lexical gap rather than a transliteration we cannot justify.
		noun = lexicalize(rt.p, rt.g, rt.entity(np), rt.l)
	}
	if noun == "" {
		if len(alts) == 0 {
			rt.reject(slot, "", HardReferentEmpty,
				"no Japanese surface form for "+string(np.EntityID), "realization")
		}
		return alts
	}
	head := noun
	if len(np.Adjectives) > 0 {
		head = strings.Join(np.Adjectives, "") + noun
	}
	for pi, p := range particles {
		if p == "" {
			p = ""
		}
		body := head + p
		alts = append(alts, forest.Alt{
			Lex: body, Probability: 0.95 / float64(1+pi), Hard: true, Rule: "<np>",
		})
		// Honorific variant of the address.
		if np.Honorific != "" {
			alts = append(alts, forest.Alt{
				Lex: head + np.Honorific + p, Probability: 0.5, Hard: true,
				Rule: "<honorific>", Note: "honorific address",
			})
		}
	}
	_ = initial
	return alts
}
