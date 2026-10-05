package plan

// constructions_data.go: the construction inventory (plan.md §26).
//
// Every entry is a frame the target language can produce for a predicate sense,
// with the discourse conditions that make it the right frame. Several
// constructions per sense is the point of the library: INTEND is not one
// English string but three, and the choice depends on register and on what the
// speaker is committed to. plan.md §26 says generation is centred on
// constructions rather than words; this is that data.
//
// Pattern vocabulary:
//
//	SUBJ   the construction's subject role (SubjRole, default agent)
//	V      the predicate (construction Lex, else the family lexicon)
//	JOIN   Join, the word before a clausal argument ("to", "that")
//	quote  reserved: render the following clause as a Japanese quoted clause
//	TAIL   Tail, a string after the predicate (つもりだ)
//	FOO    a semantic role slot, or OBJ for the direct object
//	foo    English: a preposition binding the next slot.
//	       Japanese: a case particle binding the next slot.
//
// Requires/Forbidden keys understood by Context: honorific, politeness,
// register, tense, modality, polarity, mood, aspect, direction, oral,
// recipient_defined, source_defined, speech_act.

import (
	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
)

// Fluent setters keep the table below readable.
func (c *Construction) reg(r string) *Construction          { c.Register = r; return c }
func (c *Construction) req(k, v string) *Construction       { c.Require(k, v); return c }
func (c *Construction) forb(k, v string) *Construction      { c.Forbid(k, v); return c }
func (c *Construction) prep(role, p string) *Construction   { c.PrepRole(role, p); return c }
func (c *Construction) caseOf(role, p string) *Construction { c.CaseRole(role, p); return c }
func (c *Construction) join(s string) *Construction         { c.Join = s; return c }
func (c *Construction) chain(s string) *Construction        { c.Chain = s; return c }
func (c *Construction) lex2(s string) *Construction         { c.Lex2 = s; return c }
func (c *Construction) tail(s string) *Construction         { c.Tail = s; return c }
func (c *Construction) comp(s string) *Construction         { c.Complement = s; return c }
func (c *Construction) subj(role string) *Construction      { c.SubjRole = role; return c }
func (c *Construction) pol(p string) *Construction          { c.Polarity = p; return c }
func (c *Construction) topic(m string) *Construction        { c.TopicMark = m; return c }
func (c *Construction) hon() *Construction                  { c.Honorific = true; return c }
func (c *Construction) note(s string) *Construction         { c.Notes = s; return c }

// Require adds a discourse requirement.
func (c *Construction) Require(k, v string) *Construction {
	if c.Requires == nil {
		c.Requires = map[string]string{}
	}
	c.Requires[k] = v
	return c
}

// Forbid adds a discourse prohibition.
func (c *Construction) Forbid(k, v string) *Construction {
	if c.Forbidden == nil {
		c.Forbidden = map[string]string{}
	}
	c.Forbidden[k] = v
	return c
}

// PrepRole overrides the preposition of a role.
func (c *Construction) PrepRole(role, p string) *Construction {
	if c.Prep == nil {
		c.Prep = map[string]string{}
	}
	c.Prep[role] = p
	return c
}

// CaseRole overrides the Japanese particle of a role.
func (c *Construction) CaseRole(role, p string) *Construction {
	if c.Case == nil {
		c.Case = map[string]string{}
	}
	c.Case[role] = p
	return c
}

// enC builds an English construction. The id must carry the .EN. segment: the
// library refuses a construction whose id and target disagree.
func enC(id, family, pattern, lex string, nat float64) *Construction {
	return &Construction{
		ID: id, Target: lang.EN, Senses: []string{family}, Pattern: pattern,
		Family: family, Lex: lex, Naturalness: nat, Register: RegisterNeutral,
	}
}

// jaC builds a Japanese construction. The id must carry the .JA. segment.
func jaC(id, family, pattern, form string, nat float64) *Construction {
	return &Construction{
		ID: id, Target: lang.JA, Senses: []string{family}, Pattern: pattern,
		Family: family, Lex: form, Naturalness: nat, Register: RegisterNeutral,
	}
}

// builtinConstructions returns the whole inventory. It is a function rather
// than a package variable so the library stays immutable once built.
func builtinConstructions() []*Construction {
	var out []*Construction

	// --- GIVE / transfer of possession ---------------------------------
	out = append(out,
		enC("C.GIVE.EN.01", "GIVE", "SUBJ V OBJ to RECIPIENT", "give", 0.90).
			note("double object: the recipient follows the object with to"),
		enC("C.GIVE.EN.02", "GIVE", "SUBJ V RECIPIENT OBJ", "give", 0.82).
			note("recipient first: the prepositional reading is demoted"),
		enC("C.GIVE.EN.03", "GIVE", "SUBJ V OBJ to RECIPIENT", "pass", 0.62).
			reg(RegisterFormal).note("written register prefers pass"),
		jaC("C.GIVE.JA.01", "GIVE", "SUBJ V OBJ に RECIPIENT", "渡す", 0.95).
			note("S O I order with に marking the recipient"),
		jaC("C.GIVE.JA.02", "GIVE", "SUBJ V OBJ に RECIPIENT", "あげる", 0.72).
			reg(RegisterCasual).note("colloquial giving"),
		jaC("C.GIVE.JA.03", "GIVE", "SUBJ V OBJ に RECIPIENT", "お渡しする", 0.86).
			hon().req("honorific", "1").note("honorific: the honorific verb form"),
	)

	// --- RECEIVE --------------------------------------------------------
	out = append(out,
		enC("C.RECV.EN.01", "RECEIVE", "SUBJ V OBJ from SOURCE", "receive", 0.90),
		enC("C.RECV.EN.02", "RECEIVE", "SUBJ V OBJ", "get", 0.68).
			reg(RegisterCasual).note("informal get"),
		jaC("C.RECV.JA.01", "RECEIVE", "SUBJ V OBJ から SOURCE", "受け取る", 0.92),
		jaC("C.RECV.JA.02", "RECEIVE", "SUBJ V OBJ", "もらう", 0.76).
			reg(RegisterCasual),
		jaC("C.RECV.JA.03", "RECEIVE", "SUBJ V OBJ", "いただく", 0.80).
			hon().req("honorific", "1"),
	)

	// --- TAKE ------------------------------------------------------------
	out = append(out,
		enC("C.TAKE.EN.01", "TAKE", "SUBJ V OBJ", "take", 0.86),
		enC("C.TAKE.EN.02", "TAKE", "SUBJ V OBJ from LOCATION", "take", 0.70).
			note("taken from a place rather than from a person"),
		jaC("C.TAKE.JA.01", "TAKE", "SUBJ V OBJ", "取る", 0.86),
		jaC("C.TAKE.JA.02", "TAKE", "SUBJ V OBJ を SOURCE", "取る", 0.74).
			note("resource source marked with を"),
	)

	// --- SAY -------------------------------------------------------------
	out = append(out,
		enC("C.SAY.EN.01", "SAY", "SUBJ V OBJ", "say", 0.90),
		enC("C.SAY.EN.02", "SAY", "SUBJ V OBJ to RECIPIENT", "say", 0.80).
			note("addressed speech act"),
		enC("C.SAY.EN.03", "SAY", "SUBJ V OBJ", "state", 0.55).
			reg(RegisterTechnical).note("technical register prefers state"),
		jaC("C.SAY.JA.01", "SAY", "SUBJ V OBJ", "言う", 0.90),
		jaC("C.SAY.JA.02", "SAY", "SUBJ OBJ quote V", "言う", 0.86).
			comp("quot").note("quoted speech: 〜と言った"),
		jaC("C.SAY.JA.03", "SAY", "SUBJ V OBJ", "申す", 0.80).
			hon().req("honorific", "1"),
	)

	// --- TELL ------------------------------------------------------------
	out = append(out,
		enC("C.TELL.EN.01", "TELL", "SUBJ V RECIPIENT OBJ", "tell", 0.92).
			note("tell takes an indirect object before the direct object"),
		enC("C.TELL.EN.02", "TELL", "SUBJ V RECIPIENT about OBJ", "tell", 0.60).
			note("tell somebody about something"),
		jaC("C.TELL.JA.01", "TELL", "SUBJ V OBJ に RECIPIENT", "伝える", 0.90),
		jaC("C.TELL.JA.02", "TELL", "SUBJ OBJ quote V", "伝える", 0.78).
			comp("quot").note("〜と伝えた"),
	)

	// --- ASK -------------------------------------------------------------
	out = append(out,
		enC("C.ASK.EN.01", "ASK", "SUBJ V RECIPIENT OBJ", "ask", 0.90),
		enC("C.ASK.EN.02", "ASK", "SUBJ V RECIPIENT for OBJ", "ask", 0.70).
			req("speech_act", "request").note("request rather than question"),
		jaC("C.ASK.JA.01", "ASK", "SUBJ V OBJ に RECIPIENT", "尋ねる", 0.90),
		jaC("C.ASK.JA.02", "ASK", "SUBJ V OBJ に RECIPIENT", "聞く", 0.84),
		jaC("C.ASK.JA.03", "ASK", "SUBJ V OBJ を RECIPIENT", "頼む", 0.78).
			req("speech_act", "request").note("〜をお願いする / 〜を頼む"),
	)

	// --- ANSWER ----------------------------------------------------------
	out = append(out,
		enC("C.ANSWER.EN.01", "ANSWER", "SUBJ V to RECIPIENT", "answer", 0.88),
		enC("C.ANSWER.EN.02", "ANSWER", "SUBJ V OBJ", "answer", 0.80),
		jaC("C.ANSWER.JA.01", "ANSWER", "SUBJ V RECIPIENT に", "答える", 0.90),
	)

	// --- SPEAK -----------------------------------------------------------
	out = append(out,
		enC("C.SPEAK.EN.01", "SPEAK", "SUBJ V with COMITATIVE", "speak", 0.84).
			note("speak with somebody"),
		enC("C.SPEAK.EN.02", "SPEAK", "SUBJ V about OBJ", "talk", 0.76),
		jaC("C.SPEAK.JA.01", "SPEAK", "SUBJ V と COMITATIVE", "話す", 0.86),
	)

	// --- MOTION ----------------------------------------------------------
	out = append(out,
		enC("C.MOVE.EN.01", "MOVE", "SUBJ V toward GOAL", "come", 0.92).
			req("direction", "toward").note("motion towards the speaker"),
		enC("C.MOVE.EN.02", "MOVE", "SUBJ V away from SOURCE", "go", 0.92).
			req("direction", "away").note("motion away from the speaker"),
		enC("C.MOVE.EN.06", "MOVE", "SUBJ V back to SOURCE", "return", 0.90).
			req("direction", "back").note("return to a previous location"),
		enC("C.MOVE.EN.03", "MOVE", "SUBJ V to LOCATION", "move", 0.84).
			note("motion without a deictic anchor"),
		enC("C.MOVE.EN.07", "MOVE", "SUBJ V to LOCATION", "move", 0.86).
			note("bare motion to a place"),
		enC("C.MOVE.EN.04", "MOVE", "SUBJ V from SOURCE to GOAL", "go", 0.82).
			note("motion from a source to a goal"),
		enC("C.MOVE.EN.05", "MOVE", "SUBJ V to GOAL", "walk", 0.66).
			req("tense", "present").note("deictic walk towards the speaker"),
		jaC("C.MOVE.JA.01", "MOVE", "SUBJ V に GOAL", "来る", 0.92).
			req("direction", "toward").note("motion towards a goal"),
		jaC("C.MOVE.JA.02", "MOVE", "SUBJ V から SOURCE に GOAL", "行く", 0.90),
		jaC("C.MOVE.JA.03", "MOVE", "SUBJ V から SOURCE", "行く", 0.80).
			req("direction", "away").note("departure only"),
		jaC("C.MOVE.JA.04", "MOVE", "SUBJ V に GOAL", "いらっしゃる", 0.86).
			hon().req("honorific", "1"),
	)

	// --- ARRIVE / LEAVE --------------------------------------------------
	out = append(out,
		enC("C.ARRIVE.EN.01", "ARRIVE", "SUBJ V at LOCATION", "arrive", 0.90),
		enC("C.ARRIVE.EN.02", "ARRIVE", "SUBJ V in LOCATION", "arrive", 0.70).
			note("arrival into a bounded place"),
		jaC("C.ARRIVE.JA.01", "ARRIVE", "SUBJ V に LOCATION", "着く", 0.90),
		jaC("C.ARRIVE.JA.02", "ARRIVE", "SUBJ V に LOCATION", "到着する", 0.72).
			reg(RegisterTechnical).pol("masu"),
		enC("C.LEAVE.EN.01", "LEAVE", "SUBJ V LOCATION", "leave", 0.88),
		enC("C.LEAVE.EN.02", "LEAVE", "SUBJ V", "leave", 0.74).
			note("intransitive leave"),
		jaC("C.LEAVE.JA.01", "LEAVE", "SUBJ V LOCATION を", "出る", 0.90),
		jaC("C.LEAVE.JA.02", "LEAVE", "SUBJ V", "去る", 0.80),
	)

	// --- MEET / VISIT ----------------------------------------------------
	out = append(out,
		enC("C.MEET.EN.01", "MEET", "SUBJ V OBJ", "meet", 0.92),
		enC("C.MEET.EN.02", "MEET", "SUBJ V OBJ at LOCATION", "meet", 0.70).
			note("arranged encounter with a place"),
		enC("C.MEET.EN.03", "MEET", "SUBJ V OBJ", "visit", 0.60).
			reg(RegisterFormal).note("a visit rather than an encounter"),
		jaC("C.MEET.JA.01", "MEET", "SUBJ V OBJ に", "会う", 0.90),
		jaC("C.MEET.JA.02", "MEET", "SUBJ V OBJ を", "会う", 0.78).
			note("contact with an organization"),
		jaC("C.MEET.JA.03", "MEET", "SUBJ V OBJ に", "お会いする", 0.86).
			hon().req("honorific", "1"),
	)

	// --- WANT ------------------------------------------------------------
	out = append(out,
		enC("C.WANT.EN.01", "WANT", "SUBJ V OBJ", "want", 0.90),
		enC("C.WANT.EN.02", "WANT", "SUBJ V to OBJ", "want", 0.84).
			join("to").comp("to").note("controlled predicate: want to eat"),
		enC("C.WANT.EN.03", "WANT", "SUBJ V OBJ", "would like", 0.78).
			note("more polite than want"),
		jaC("C.WANT.JA.01", "WANT", "SUBJ V OBJ", "欲しい", 0.92).
			note("desiderative adjective"),
		jaC("C.WANT.JA.02", "WANT", "SUBJ V OBJ", "ほしい", 0.74).
			pol("plain").note("plain written form of 欲しい"),
		jaC("C.WANT.JA.03", "WANT", "SUBJ OBJ を V", "食べ", 0.88).
			chain("te").lex2("たい").note("〜たい: first and second person only"),
	)

	// --- INTEND / PLAN ---------------------------------------------------
	out = append(out,
		enC("C.INTEND.EN.01", "INTEND", "SUBJ V JOIN OBJ", "intend", 0.88).
			join("to").comp("to").note("intends to Y"),
		enC("C.INTEND.EN.02", "INTEND", "SUBJ V JOIN OBJ", "mean", 0.78).
			join("to").comp("to").note("means to Y"),
		enC("C.INTEND.EN.03", "INTEND", "SUBJ V JOIN OBJ", "plan", 0.82).
			join("to").comp("to").note("plans to Y"),
		enC("C.INTEND.EN.04", "INTEND", "SUBJ V OBJ", "intend", 0.70).
			note("intention towards an entity rather than an event"),
		jaC("C.INTEND.JA.01", "INTEND", "SUBJ V OBJ TAIL", "", 0.90).
			tail("つもりだ").note("〜するつもりだ"),
		jaC("C.INTEND.JA.02", "INTEND", "SUBJ V OBJ TAIL", "", 0.78).
			tail("意図がある").note("〜する意図がある"),
		jaC("C.INTEND.JA.03", "INTEND", "SUBJ V OBJ を", "打算する", 0.62).
			reg(RegisterTechnical),
		jaC("C.INTEND.JA.04", "INTEND", "SUBJ OBJ quote V", "思う", 0.84).
			comp("quot").note("〜しようと思っている"),
	)

	// --- BE / EXIST / NAMED ----------------------------------------------
	out = append(out,
		enC("C.BE.EN.01", "BE", "SUBJ V OBJ", "be", 0.70).
			note("copular predication"),
		jaC("C.BE.JA.01", "BE", "SUBJ V", "いる", 0.86).
			note("animate existence / location"),
		jaC("C.BE.JA.02", "BE", "SUBJ V に LOCATION", "いる", 0.90).
			note("existence at a place"),
		jaC("C.BE.JA.03", "BE", "SUBJ V", "ある", 0.84).
			subj(jlir.RolePatient).note("inanimate existence, patient as subject"),
		enC("C.EXIST.EN.01", "EXIST", "V OBJ", "there is", 0.90).
			subj(jlir.RolePatient).note("existential there"),
		jaC("C.EXIST.JA.01", "EXIST", "SUBJ V", "ある", 0.92).
			subj(jlir.RolePatient),
		enC("C.NAMED.EN.01", "NAMED", "SUBJ V OBJ", "be called", 0.94).
			note("naming: X is called Y"),
		enC("C.NAMED.EN.02", "NAMED", "SUBJ V OBJ", "be named", 0.78).
			reg(RegisterFormal),
		jaC("C.NAMED.JA.03", "NAMED", "SUBJ V OBJ TAIL", "", 0.84).
			tail("という名前だ").note("〜という名前だ"),
		jaC("C.NAMED.JA.02", "NAMED", "SUBJ V OBJ", "だ", 0.70).
			pol("da").note("copular identification"),
	)

	// --- BECOME ----------------------------------------------------------
	out = append(out,
		enC("C.BECOME.EN.01", "BECOME", "SUBJ V OBJ", "become", 0.92),
		jaC("C.BECOME.JA.01", "BECOME", "SUBJ V OBJ に", "なる", 0.92),
	)

	// --- HAVE / POSSESS --------------------------------------------------
	out = append(out,
		enC("C.HAVE.EN.01", "HAVE", "SUBJ V OBJ", "have", 0.92),
		enC("C.HAVE.EN.02", "HAVE", "SUBJ V got OBJ", "have", 0.70).
			reg(RegisterCasual).note("have got, spoken"),
		enC("C.HAVE.EN.03", "HAVE", "SUBJ V OBJ", "own", 0.84),
		jaC("C.HAVE.JA.01", "HAVE", "SUBJ V OBJ", "持つ", 0.92),
		jaC("C.HAVE.JA.02", "HAVE", "SUBJ の OBJ", "持つ", 0.84).
			note("〜の: nominal genitive possession"),
		jaC("C.HAVE.JA.03", "HAVE", "SUBJ V OBJ", "お持ちする", 0.80).
			hon().req("honorific", "1"),
	)

	// --- ABLE / MUST / SHOULD / MAY --------------------------------------
	out = append(out,
		enC("C.ABLE.EN.01", "ABLE", "SUBJ V JOIN OBJ", "be able", 0.84).
			join("to").comp("to").note("is able to eat"),
		enC("C.ABLE.EN.02", "ABLE", "SUBJ V OBJ", "can", 0.90).
			note("dynamic modal"),
		jaC("C.ABLE.JA.01", "ABLE", "SUBJ V OBJ を V", "食べる", 0.92).
			chain("te").lex2("られる").note("potential: 〜られる"),
		jaC("C.ABLE.JA.02", "ABLE", "SUBJ V OBJ を V", "出来る", 0.84).
			note("出来る / できる as a lexical predicate"),
		enC("C.MUST.EN.01", "MUST", "SUBJ V OBJ", "must", 0.92),
		enC("C.MUST.EN.02", "MUST", "SUBJ V OBJ", "have to", 0.78).
			note("external obligation"),
		jaC("C.MUST.JA.01", "MUST", "SUBJ OBJ を V TAIL", "読ま", 0.92).
			chain("nai_n").tail("ければならない").note("〜なければならない"),
		jaC("C.MUST.JA.02", "MUST", "SUBJ OBJ を V TAIL", "しちゃ", 0.62).
			reg(RegisterCasual).chain("te").tail("いけない").note("〜しちゃいけない"),
		enC("C.SHOULD.EN.01", "SHOULD", "SUBJ V JOIN OBJ", "should", 0.90).
			join("to").comp("to"),
		enC("C.SHOULD.EN.02", "SHOULD", "SUBJ V OBJ", "ought to", 0.70).
			reg(RegisterFormal),
		jaC("C.SHOULD.JA.01", "SHOULD", "SUBJ OBJ を V TAIL", "食べ", 0.90).
			chain("masu").tail("るべきだ").note("〜るべきだ"),
		jaC("C.SHOULD.JA.02", "SHOULD", "SUBJ OBJ を V TAIL", "食べ", 0.72).
			chain("te").tail("たほうがいい").note("〜たほうがいい"),
		enC("C.MAY.EN.01", "MAY", "SUBJ V OBJ", "may", 0.90),
		enC("C.MAY.EN.02", "MAY", "SUBJ V OBJ", "be allowed to", 0.74).
			join("").comp("to").note("permission"),
		jaC("C.MAY.JA.01", "MAY", "SUBJ V OBJ", "よい", 0.84).
			note("〜てよい / 〜てもいい"),
	)

	// --- EAT / DRINK -----------------------------------------------------
	out = append(out,
		enC("C.EAT.EN.01", "EAT", "SUBJ V OBJ", "eat", 0.94).
			req("ingestible", "solid").note("solid food only: a liquid theme is DRINK"),
		enC("C.EAT.EN.02", "EAT", "SUBJ V OBJ", "have", 0.66).
			req("ingestible", "solid").
			reg(RegisterCasual).note("have something to eat"),
		jaC("C.EAT.JA.01", "EAT", "SUBJ V OBJ を", "食べる", 0.94).
			req("ingestible", "solid").note("solid food only: a liquid theme is 飲む"),
		jaC("C.EAT.JA.02", "EAT", "SUBJ V OBJ を", "いただく", 0.84).
			req("ingestible", "solid").hon().req("honorific", "1"),
		enC("C.DRINK.EN.01", "DRINK", "SUBJ V OBJ", "drink", 0.94).
			req("ingestible", "liquid").note("liquid only: a solid theme is EAT"),
		jaC("C.DRINK.JA.01", "DRINK", "SUBJ V OBJ を", "飲む", 0.94).
			req("ingestible", "liquid").note("liquid only: a solid theme is 食べる"),
		jaC("C.DRINK.JA.02", "DRINK", "SUBJ V OBJ を", "いただく", 0.84).
			req("ingestible", "liquid").hon().req("honorific", "1"),
	)

	// --- SEE / HEAR ------------------------------------------------------
	out = append(out,
		enC("C.SEE.EN.01", "SEE", "SUBJ V OBJ", "see", 0.92),
		enC("C.SEE.EN.02", "SEE", "SUBJ V OBJ", "watch", 0.72).
			note("deliberate watching"),
		enC("C.SEE.EN.03", "SEE", "SUBJ V OBJ", "look at", 0.70),
		jaC("C.SEE.JA.01", "SEE", "SUBJ V OBJ を", "見る", 0.94),
		jaC("C.SEE.JA.02", "SEE", "SUBJ V OBJ を", "観る", 0.70).
			note("watching a performance"),
		jaC("C.SEE.JA.03", "SEE", "SUBJ V OBJ を", "ご覧になる", 0.84).
			hon().req("honorific", "1"),
		enC("C.HEAR.EN.01", "HEAR", "SUBJ V OBJ", "hear", 0.92),
		enC("C.HEAR.EN.02", "HEAR", "SUBJ V OBJ from SOURCE", "hear", 0.78),
		jaC("C.HEAR.JA.01", "HEAR", "SUBJ V OBJ を", "聞く", 0.90),
		jaC("C.HEAR.JA.02", "HEAR", "SUBJ V OBJ", "聞こえる", 0.82).
			note("the sound reaches the experiencer"),
	)

	// --- KNOW / THINK / BELIEVE ------------------------------------------
	out = append(out,
		enC("C.KNOW.EN.01", "KNOW", "SUBJ V OBJ", "know", 0.92),
		enC("C.KNOW.EN.02", "KNOW", "SUBJ V JOIN OBJ", "know", 0.84).
			join("that").comp("that").note("know that he said"),
		jaC("C.KNOW.JA.01", "KNOW", "SUBJ V OBJ を", "知る", 0.92),
		jaC("C.KNOW.JA.02", "KNOW", "SUBJ OBJ quote V", "知る", 0.74).
			comp("quot").note("〜と知っている"),
		enC("C.THINK.EN.01", "THINK", "SUBJ V OBJ", "think", 0.86),
		enC("C.THINK.EN.02", "THINK", "SUBJ V JOIN OBJ", "think", 0.84).
			join("that").comp("that"),
		enC("C.THINK.EN.03", "THINK", "SUBJ V about OBJ", "think", 0.66),
		jaC("C.THINK.JA.01", "THINK", "SUBJ V OBJ と", "思う", 0.90).
			comp("quot").note("〜と思う"),
		jaC("C.THINK.JA.02", "THINK", "SUBJ OBJ quote V", "考える", 0.78).
			comp("quot").note("〜と考える"),
		enC("C.BELIEVE.EN.01", "BELIEVE", "SUBJ V OBJ", "believe", 0.90),
		enC("C.BELIEVE.EN.02", "BELIEVE", "SUBJ V JOIN OBJ", "believe", 0.78).
			join("that").comp("that"),
		jaC("C.BELIEVE.JA.01", "BELIEVE", "SUBJ OBJ quote V", "思う", 0.84).
			comp("quot").note("〜と思う"),
	)

	// --- COMPARE ---------------------------------------------------------
	out = append(out,
		enC("C.CMP.EN.01", "COMPARE", "SUBJ V OBJ with STIMULUS", "compare", 0.92),
		enC("C.CMP.EN.02", "COMPARE", "SUBJ V OBJ to STIMULUS", "compare", 0.84).
			note("comparison against a standard"),
		enC("C.CMP.EN.03", "COMPARE", "SUBJ V OBJ", "be like", 0.66).
			note("similarity without an explicit standard"),
		jaC("C.CMP.JA.01", "COMPARE", "SUBJ V OBJ と STIMULUS を", "比べる", 0.90),
		jaC("C.CMP.JA.02", "COMPARE", "SUBJ V OBJ に", "似ている", 0.84).
			note("〜に似ている"),
		jaC("C.CMP.JA.03", "COMPARE", "SUBJ V OBJ と", "同じ", 0.76).
			note("〜と同じ"),
	)

	// --- COPULA ----------------------------------------------------------
	out = append(out,
		enC("C.COPULA.EN.01", "COPULA", "SUBJ V OBJ", "be", 0.80),
		enC("C.COPULA.EN.02", "COPULA", "SUBJ V OBJ", "remain", 0.50).
			reg(RegisterLiterary),
		jaC("C.COPULA.JA.01", "COPULA", "SUBJ V", "だ", 0.70).
			pol("da").note("plain copula"),
		jaC("C.COPULA.JA.02", "COPULA", "SUBJ V", "である", 0.74).
			pol("da").reg(RegisterFormal).note("written copula"),
		jaC("C.COPULA.JA.03", "COPULA", "SUBJ V OBJ", "だ", 0.80).
			pol("da").note("〜だ / 〜です"),
	)

	// --- misc high frequency families -------------------------------------
	out = append(out,
		enC("C.LIKE.EN.01", "LIKE", "SUBJ V OBJ", "like", 0.90),
		jaC("C.LIKE.JA.01", "LIKE", "SUBJ V OBJ が", "好き", 0.90).
			note("〜が好き"),
		jaC("C.LIKE.JA.02", "LIKE", "SUBJ V OBJ を", "嫌い", 0.84).
			note("〜を嫌い"),
		enC("C.SHOW.EN.01", "SHOW", "SUBJ V OBJ to RECIPIENT", "show", 0.92),
		jaC("C.SHOW.JA.01", "SHOW", "SUBJ V OBJ に RECIPIENT", "見せる", 0.90),
		enC("C.READ.EN.01", "READ", "SUBJ V OBJ", "read", 0.92),
		jaC("C.READ.JA.01", "READ", "SUBJ V OBJ を", "読む", 0.92),
		enC("C.WRITE.EN.01", "WRITE", "SUBJ V OBJ", "write", 0.92),
		jaC("C.WRITE.JA.01", "WRITE", "SUBJ V OBJ に", "書く", 0.92),
		enC("C.SLEEP.EN.01", "SLEEP", "SUBJ V", "sleep", 0.92),
		jaC("C.SLEEP.JA.01", "SLEEP", "SUBJ V", "寝る", 0.92),
		enC("C.LIVE.EN.01", "LIVE", "SUBJ V in LOCATION", "live", 0.90),
		jaC("C.LIVE.JA.01", "LIVE", "SUBJ V に LOCATION", "住む", 0.90),
		enC("C.WORK.EN.01", "WORK", "SUBJ V", "work", 0.90),
		jaC("C.WORK.JA.01", "WORK", "SUBJ V", "働く", 0.90),
		enC("C.OPEN.EN.01", "OPEN", "SUBJ V OBJ", "open", 0.90),
		jaC("C.OPEN.JA.01", "OPEN", "SUBJ V OBJ を", "開く", 0.90),
		enC("C.CLOSE.EN.01", "CLOSE", "SUBJ V OBJ", "close", 0.90),
		jaC("C.CLOSE.JA.01", "CLOSE", "SUBJ V OBJ を", "閉める", 0.90),
		enC("C.START.EN.01", "START", "SUBJ V OBJ", "start", 0.90),
		jaC("C.START.JA.01", "START", "SUBJ V OBJ を", "始める", 0.90),
		enC("C.STOP.EN.01", "STOP", "SUBJ V OBJ", "stop", 0.90),
		jaC("C.STOP.JA.01", "STOP", "SUBJ V OBJ を", "やめる", 0.90),
		enC("C.SEND.EN.01", "SEND", "SUBJ V OBJ to RECIPIENT", "send", 0.92),
		jaC("C.SEND.JA.01", "SEND", "SUBJ V OBJ に RECIPIENT", "送る", 0.92),
		enC("C.BUY.EN.01", "BUY", "SUBJ V OBJ from SOURCE", "buy", 0.92),
		jaC("C.BUY.JA.01", "BUY", "SUBJ V OBJ を SOURCE から", "買う", 0.92),
		enC("C.SELL.EN.01", "SELL", "SUBJ V OBJ to RECIPIENT", "sell", 0.92).
			note("selling keeps the buyer as recipient, never the reverse"),
		jaC("C.SELL.JA.01", "SELL", "SUBJ V OBJ を RECIPIENT に", "売る", 0.92),
		jaC("C.SELL.JA.02", "SELL", "SUBJ V OBJ を RECIPIENT に", "お売りする", 0.84).
			hon().req("honorific", "1"),
	)

	// --- nominal / auxiliary families used by constructions ---------------
	out = append(out,
		enC("C.NOMINAL.EN.01", "NOMINAL", "V", "", 0.50).
			note("empty predicate: the frame carries the tail only"),
		jaC("C.NOMINAL.JA.01", "NOMINAL", "V", "", 0.50).
			note("empty predicate: と思う frames are built from it"),
	)

	return out
}

// enPrep returns the preposition English conventionally uses for a role. The
// realizer overrides it with a construction specific Prep entry.
func enPrep(role string) string {
	switch role {
	case jlir.RoleRecipient:
		return "to"
	case jlir.RoleGoal:
		return "to"
	case jlir.RoleSource:
		return "from"
	case jlir.RoleLocation:
		return "at"
	case jlir.RoleInstrument:
		return "with"
	case jlir.RoleComitative:
		return "with"
	case jlir.RoleTime:
		return "at"
	case jlir.RoleManner:
		return "with"
	case jlir.RoleCause:
		return "because of"
	case jlir.RoleBeneficiary:
		return "for"
	case jlir.RolePossessor:
		return "of"
	case jlir.RoleProduct:
		return ""
	}
	return ""
}
