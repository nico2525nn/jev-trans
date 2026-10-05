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
type jpVerb struct {
	dic string // dictionary form
	cls string // conjugation class
	hon string // honorific form, "" when the verb has none
	hum string // humble form
}

// Japanese conjugation classes.
const (
	jpIchidan = "i"   // 食べる, 見る, いる
	jpU       = "u"   // 買う, 言う, 行く
	jpKu      = "ku"  // 書く, 聞く
	jpGu      = "gu"  // 泳ぐ
	jpSu      = "su"  // 出す, 話す-like godan verbs
	jpTsu     = "tsu" // 立つ, 待つ, 持つ
	jpNu      = "nu"  // 死ぬ
	jpBu      = "bu"  // 飛ぶ, 遊ぶ
	jpMu      = "mu"  // 読む, 飲む
	jpRu      = "ru"  // 作る, 取る, 帰る, 言う-like
	jpSuru    = "s"   // する
	jpKuru    = "k"   // 来る
	jpCopula  = "da"  // だ, である
	jpIAdj    = "adj" // いい, 欲しい, 大きい
	jpNaAdj   = "na"  // 好き, 静か, same
	jpNominal = "nom" // べき, こと
)

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
	"SAY":     {dic: "言う", cls: jpU, hon: "おっしゃる", hum: "申す"},
	"TELL":    {dic: "伝える", cls: jpRu},
	"ASK":     {dic: "尋ねる", cls: jpRu},
	"ANSWER":  {dic: "答える", cls: jpRu},
	"SPEAK":   {dic: "話す", cls: jpU, hon: "おっしゃる"},
	"MOVE":    {dic: "行く", cls: jpU, hon: "いらっしゃる"},
	"ARRIVE":  {dic: "着く", cls: jpKu, hon: "いらっしゃる"},
	"LEAVE":   {dic: "出る", cls: jpRu, hon: "いらっしゃる"},
	"MEET":    {dic: "会う", cls: jpU, hon: "お会いする"},
	"WANT":    {dic: "欲しい", cls: jpIAdj},
	"INTEND":  {dic: "思う", cls: jpU},
	"BE":      {dic: "いる", cls: jpRu, hon: "いらっしゃる"},
	"EXIST":   {dic: "ある", cls: jpRu, hum: "ございます"},
	"NAMED":   {dic: "言う", cls: jpU, hon: "おっしゃる"},
	"BECOME":  {dic: "なる", cls: jpRu, hon: "いらっしゃる"},
	"HAVE":    {dic: "持つ", cls: jpTsu, hon: "お持ちする"},
	"ABLE":    {dic: "出来る", cls: jpIchidan},
	"MUST":    {dic: "", cls: jpNominal},
	"SHOULD":  {dic: "", cls: jpNominal},
	"MAY":     {dic: "いい", cls: jpIAdj},
	"EAT":     {dic: "食べる", cls: jpIchidan, hon: "いらっしゃる", hum: "いただく"},
	"DRINK":   {dic: "飲む", cls: jpMu, hon: "いらっしゃる", hum: "いただく"},
	"SEE":     {dic: "見る", cls: jpIchidan, hon: "ご覧になる"},
	"HEAR":    {dic: "聞く", cls: jpKu},
	"KNOW":    {dic: "知る", cls: jpRu, hon: "ご存じになる"},
	"THINK":   {dic: "思う", cls: jpU},
	"BELIEVE": {dic: "思う", cls: jpU},
	"COMPARE": {dic: "比べる", cls: jpRu},
	"COPULA":  {dic: "だ", cls: jpCopula},
	"NOMINAL": {dic: "", cls: jpNominal},
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

// stem returns the 連用形 (masu stem) of a verb: the form polite ます attaches
// to, and the base of the ichidan te-form.
func (v jpVerb) stem() string {
	switch v.cls {
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
		return v.negStem() + r
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
// and honorific all come from the plan, never from the surface.
func jpConjugate(v jpVerb, ep *EventPlan, quoted bool) string {
	polite := ep != nil && ep.Politeness >= 0.5
	negative := ep != nil && strings.EqualFold(ep.Polarity, jlir.PolarityNegative)
	past := ep != nil && ep.Tense == jlir.TensePast

	// Honorific: the addressee directed form replaces the verb entirely. A
	// quoted clause is never replaced: 「〜と思う」 must keep what is quoted.
	if ep != nil && ep.Honorific && !quoted {
		if v.hon != "" {
			if past {
				if polite {
					return v.hon + "ました"
				}
				return v.hon + "た"
			}
			if polite {
				return v.hon + "ます"
			}
			return v.hon
		}
	}
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
		if sfp := rt.p.SentenceFinal; sfp != "" && usedV && rt.clauseFinal {
			parts = append(parts, piece{label: "SFP",
				alts: []forest.Alt{{Lex: sfp, Probability: 0.85, Hard: true}}})
		}
		if !rt.clauseFinal {
			parts = append(parts, piece{label: "PUNCT",
				alts: []forest.Alt{{Lex: rt.punctuation(rt.p), Probability: 0.9, Hard: true}}})
		}
		parts = append(parts, rt.idPiece(c))
		if len(parts) <= 1 {
			continue
		}
		out = append(out, frame{c: c, parts: parts})
	}
	return out
}

// patternJP walks a Japanese construction pattern. A bare token is a case
// particle or connective binding the next slot; SUBJ takes the topic/subject
// marker the projection decided; "quote" folds the embedded clause into the
// predicate, because Japanese puts 〜と before the verb.
func (rt *realizer) patternJP(ep *EventPlan, c *Construction, depth int, usedV *bool) []piece {
	var parts []piece
	toks := sovOrder(tokens(c.Pattern))
	particle := ""
	seenV := false
	var quotText string
	add := func(p piece) {
		if p.empty() {
			return
		}
		parts = append(parts, p)
	}
	for _, tok := range toks {
		switch tok {
		case "SUBJ":
			if rt.noSubject {
				continue
			}
			np := resolveSubject(ep, c)
			npParticle := ""
			if np != nil {
				npParticle = np.Particle
			}
			p := firstNonEmpty(particle, npParticle, c.TopicMark, Ga)
			subject := rt.jpNPPiece(np, "SUBJ", p, false, depth, c)
			particle = ""
			add(subject)
		case "V":
			if seenV && c.Lex2 == "" {
				continue
			}
			alts := rt.jpVerbPiece(ep, c, depth, seenV, quotText)
			seenV = true
			*usedV = true
			if len(alts) > 0 {
				parts = append(parts, piece{label: "V", alts: alts})
			}
			if !seenV && c.Lex2 != "" && c.Chain != "" {
				// A chain construction continues past the object: 食べたい.
				parts = append(parts, piece{label: "TAIL",
					alts: []forest.Alt{{Lex: c.Lex2, Probability: c.Naturalness, Hard: true}}})
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
			np := complementArg(ep)
			if np != nil && np.Clause != nil {
				quotText = rt.subClauseText(np.Clause, depth)
			}
		case "OBJ":
			role := objectRole(ep)
			np := ep.Args[role]
			if np != nil && np.IsClause {
				if c.Complement == "quot" || c.Chain != "" {
					// Folded into the predicate by the "quote" token or by the
					// thematic verb chain: the clausal argument is the verb, not
					// a phrase, so it is not realized as a subtree here.
					continue
				}
				// Japanese puts 〜と before the verb, so a clausal argument can
				// only be realized by a quoting or a chain construction.
				rt.reject(string(ep.EventID), c.ID, HardMissingArg,
					"construction "+c.ID+" cannot realize a clausal argument in Japanese", "realization")
				return nil
			}
			npParticle := ""
			if np != nil {
				npParticle = np.Particle
			}
			p := firstNonEmpty(particle, c.Case[role], npParticle, Wo)
			obj := rt.jpNPPiece(np, "OBJ", p, false, depth, c)
			particle = ""
			add(obj)
		default:
			if isSlotToken(tok) {
				role := roleOfToken(tok)
				np := ep.Args[role]
				if np != nil && np.IsClause && (c.Complement == "quot" || c.Chain != "") {
					particle = ""
					continue
				}
				npParticle := ""
				if np != nil {
					npParticle = np.Particle
				}
				p := firstNonEmpty(particle, c.Case[role], npParticle, "")
				slot := rt.jpNPPiece(np, tok, p, false, depth, c)
				particle = ""
				add(slot)
				continue
			}
			particle = tok
		}
	}
	return parts
}

// jpNPPiece returns the alternatives of one argument slot.
func sovOrder(toks []string) []string {
	vAt := -1
	for i, t := range toks {
		if t == "V" {
			vAt = i
			break
		}
	}
	if vAt < 0 {
		return toks
	}
	alreadySOV := true
	for _, t := range toks[vAt+1:] {
		if isJPArgumentToken(t) {
			alreadySOV = false
			break
		}
	}
	if alreadySOV {
		return toks
	}
	out := make([]string, 0, len(toks))
	out = append(out, toks[:vAt]...)
	for _, t := range toks[vAt+1:] {
		if isJPArgumentToken(t) {
			out = append(out, t)
		}
	}
	out = append(out, "V")
	for _, t := range toks[vAt+1:] {
		if !isJPArgumentToken(t) {
			out = append(out, t)
		}
	}
	return out
}

// isJPArgumentToken reports whether a pattern token names an argument that must
// precede the verb.
func isJPArgumentToken(t string) bool {
	switch t {
	case "SUBJ", "OBJ", "MID":
		return true
	}
	return isSlotToken(t)
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

// firstNonEmpty returns the first non-empty argument.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// subClauseText realizes an embedded clause as one string. Japanese quoting
// places the clause inside the predicate (〜と思う), which the packed forest
// cannot express as a prefix, so the embedded clause is realized into its best
// string first and interned as a single terminal.
func (rt *realizer) subClauseText(ep *EventPlan, depth int) string {
	if ep == nil || depth >= maxEmbedDepth {
		return ""
	}
	tmp := forest.NewBuilder("q")
	sub := &realizer{r: rt.r, p: rt.p, g: rt.g, gp: rt.gp, b: tmp, out: rt.out, l: rt.l}
	node := sub.clauseNode(ep, false, depth+1)
	if node == "" {
		return ""
	}
	trees := tmp.Root(node).Enumerate(1)
	if len(trees) == 0 {
		return ""
	}
	return trees[0].Text()
}

// jpVerbPiece builds the predicate of one Japanese clause: tense, polarity,
// politeness, honorific, aspect and completion all derive from the plan.
func (rt *realizer) jpVerbPiece(ep *EventPlan, c *Construction, depth int, second bool, quotText string) []forest.Alt {
	// The second V slot carries the construction's second predicate piece
	// (られる in 〜食べられる).
	if second {
		if c.Lex2 == "" {
			return nil
		}
		return []forest.Alt{{Lex: c.Lex2, Probability: c.Naturalness, Rule: "<" + c.ID + ">", Hard: true}}
	}
	if c.Chain != "" {
		return rt.jpChainPiece(ep, c)
	}
	v := lookupJPVerb(c.Lex, c.Family)
	if quotText != "" {
		v.cls = jpU
		v.dic = quotText + "と"
	}
	form := jpConjugate(v, ep, quotText != "")
	if form == "" {
		if quotText == "" {
			rt.reject(string(ep.EventID), c.Lex, HardUnknownSense,
				"no Japanese form for "+c.Family, "realization")
			return nil
		}
		form = quotText + "と"
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
	case "masu", "te":
		stem = theme.stem()
	case "tai":
		stem = theme.te()
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

// jpVerbClass reports whether a literal surface is a known verb form.
func jpVerbClass(lex string) (jpVerb, bool) {
	if v, ok := jpFamilyVerb[lex]; ok {
		return v, true
	}
	return jpVerb{}, false
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
	"話す": jpU, "行く": jpU, "着く": jpKu, "到着する": jpSuru,
	"出る": jpRu, "去る": jpRu, "会う": jpU, "お会いする": jpSuru,
	"欲しい": jpIAdj, "ほしい": jpIAdj, "たい": jpIAdj,
	"思う": jpU, "考える": jpRu, "打算する": jpSuru,
	"いる": jpRu, "いらっしゃる": jpU, "ある": jpRu, "なる": jpRu,
	"持つ": jpTsu, "お持ちする": jpSuru, "できる": jpIchidan, "出来る": jpIchidan,
	"いい": jpIAdj, "よい": jpIAdj, "食べる": jpIchidan, "観る": jpIchidan,
	"飲む": jpMu, "見る": jpIchidan, "ご覧になる": jpIAdj, "聞こえる": jpIchidan,
	"知る": jpRu, "ご存じになる": jpIAdj, "比べる": jpRu, "似ている": jpIchidan,
	"同じ": jpIAdj, "好き": jpNaAdj, "嫌い": jpNaAdj, "見せる": jpRu,
	"読む": jpMu, "書く": jpKu, "寝る": jpRu, "住む": jpMu, "働く": jpKu,
	"開く": jpKu, "閉める": jpRu, "始める": jpRu, "やめる": jpRu,
	"送る": jpRu, "買う": jpU, "売る": jpRu, "お売りする": jpSuru,
	"使う": jpU, "立つ": jpTsu, "死ぬ": jpNu, "飛ぶ": jpBu, "作る": jpRu,
	"帰る": jpRu, "出す": jpSu, "泳ぐ": jpGu, "泣く": jpKu, "急ぐ": jpGu,
	"待つ": jpTsu, "遊ぶ": jpBu, "知る人": jpRu, "始まる": jpRu,
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
	particles := []string{particle}
	if np.Particle != "" && np.Particle != particle {
		particles = append(particles, np.Particle)
	}

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
