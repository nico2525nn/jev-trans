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
//	quote  render the clausal argument as a quoted clause inside the predicate
//	TAIL   Tail, a string after the predicate (つもりだ)
//	BARE   realize the following slot with no case particle at all
//	OBJ    the direct object, or the clausal argument a quote/chain frame
//	       folds into its predicate
//	ROLE   a semantic role slot, or OBJ for the direct object
//
// A lowercase token is a binder, and the two languages bind in opposite
// directions because the two languages order their constituents in opposite
// directions:
//
//	English   the preposition precedes the slot it binds:
//	          "SUBJ V OBJ to RECIPIENT".
//	Japanese  the case particle follows the noun phrase it marks:
//	          "SUBJ OBJ を RECIPIENT に V". Japanese is verb final, so a pattern
//	          that lists the arguments after the verb is not a Japanese sentence.
//	          That used to be papered over by reordering the pattern at
//	          realization time, which moved every case particle and every TAIL
//	          away from the word it belongs to; the data below states Japanese
//	          in Japanese order and no reordering happens.
//
// The case particle is a fact about the construction, not about the discourse,
// so a particle written in the pattern is the particle that is used (plan.md
// §36: Semantics(T) ⊇ RequiredMeaning). BARE is how a frame says the slot takes
// no particle, for the slots that do not take one (「本だ」, 「〜という名前だ」).
// The one slot whose particle the projection owns is SUBJ: は (topic) versus
// が (subject) is an information-structure decision (plan.md §19) and no
// construction states it.
//
// Chain constructions continue the thematic verb of a clausal argument
// ("食べなければならない"): Chain names how the stem is derived and Tail
// supplies the rest of the ending. The argument's own noun phrase is realized
// in the matrix frame as the object, because Japanese writes it there.
//
// Requires/Forbidden keys understood by Context: honorific, politeness,
// register, tense, modality, polarity, mood, aspect, direction, oral,
// recipient_defined, source_defined, speech_act.

import (
	"github.com/nico/jev-trans/internal/jlir"
	"github.com/nico/jev-trans/internal/lang"
)

// Fluent setters keep the table below readable.
func (c *Construction) reg(r string) *Construction     { c.Register = r; return c }
func (c *Construction) req(k, v string) *Construction  { c.Require(k, v); return c }
func (c *Construction) forb(k, v string) *Construction { c.Forbid(k, v); return c }
func (c *Construction) join(s string) *Construction    { c.Join = s; return c }
func (c *Construction) chain(s string) *Construction   { c.Chain = s; return c }
func (c *Construction) tail(s string) *Construction    { c.Tail = s; return c }
func (c *Construction) comp(s string) *Construction    { c.Complement = s; return c }
func (c *Construction) subj(role string) *Construction { c.SubjRole = role; return c }
func (c *Construction) pol(p string) *Construction     { c.Polarity = p; return c }
func (c *Construction) hon() *Construction             { c.Honorific = true; return c }
func (c *Construction) note(s string) *Construction    { c.Notes = s; return c }

// senses narrows the ontology senses this construction realizes. Without it a
// construction covers every sense its family is registered for, which is right
// for MOVE and wrong for EAT (CONSUME.02 is a liquid, which 食べる cannot take).
// plan.md §36: Semantics(T) ⊇ RequiredMeaning decides which frame may be used
// for a sense, not the other way round.
func (c *Construction) senses(ids ...string) *Construction {
	c.Senses = append([]string(nil), ids...)
	return c
}

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
//
// Every Japanese pattern below is written in Japanese order: the arguments
// first, each followed by its case particle, then the predicate, then any tail.
// "SUBJ OBJ を RECIPIENT に V" is 太郎は本を花子に渡す; the same data written as
// "SUBJ V OBJ に RECIPIENT" could only be salvaged by reordering the pattern at
// realization time, which is what produced 「太郎は本を花子に渡るます」 before.
func builtinConstructions() []*Construction {
	var out []*Construction

	// --- GIVE / transfer of possession -------------------------------------
	out = append(out,
		enC("C.GIVE.EN.01", "GIVE", "SUBJ V OBJ to RECIPIENT", "give", 0.90).
			senses("TRANSFER.01", "TRANSFER.03", "TRANSFER.07", "TRANSFER.14").
			note("double object: the recipient follows the object with to"),
		enC("C.GIVE.EN.02", "GIVE", "SUBJ V RECIPIENT OBJ", "give", 0.82).
			senses("TRANSFER.01").
			note("recipient first: the prepositional reading is demoted"),
		enC("C.GIVE.EN.03", "GIVE", "SUBJ V OBJ to RECIPIENT", "pass", 0.62).
			senses("TRANSFER.14").reg(RegisterFormal).note("written register prefers pass"),
		jaC("C.GIVE.JA.01", "GIVE", "SUBJ OBJ を RECIPIENT に V", "渡す", 0.95).
			senses("TRANSFER.01", "TRANSFER.03", "TRANSFER.07", "TRANSFER.10", "TRANSFER.14").
			note("S O I order with を marking the object and に the recipient"),
		jaC("C.GIVE.JA.02", "GIVE", "SUBJ OBJ を RECIPIENT に V", "あげる", 0.72).
			senses("TRANSFER.01", "TRANSFER.07").
			reg(RegisterCasual).note("colloquial giving"),
		jaC("C.GIVE.JA.03", "GIVE", "SUBJ OBJ を RECIPIENT に V", "お渡しする", 0.86).
			senses("TRANSFER.01", "TRANSFER.10").
			hon().req("honorific", "1").note("honorific: the honorific verb form"),
	)

	// --- RECEIVE ------------------------------------------------------------
	out = append(out,
		enC("C.RECV.EN.01", "RECEIVE", "SUBJ V OBJ from SOURCE", "receive", 0.90).
			senses("TRANSFER.09", "TRANSFER.04"),
		enC("C.RECV.EN.02", "RECEIVE", "SUBJ V OBJ", "get", 0.68).
			senses("TRANSFER.09").reg(RegisterCasual).note("informal get"),
		jaC("C.RECV.JA.01", "RECEIVE", "SUBJ OBJ を SOURCE から V", "受け取る", 0.92).
			senses("TRANSFER.09", "TRANSFER.04"),
		jaC("C.RECV.JA.02", "RECEIVE", "SUBJ OBJ を V", "もらう", 0.76).
			senses("TRANSFER.09", "TRANSFER.04").reg(RegisterCasual),
		jaC("C.RECV.JA.03", "RECEIVE", "SUBJ OBJ を V", "いただく", 0.80).
			senses("TRANSFER.09").hon().req("honorific", "1"),
	)

	// --- TAKE --------------------------------------------------------------
	out = append(out,
		enC("C.TAKE.EN.01", "TAKE", "SUBJ V OBJ", "take", 0.86).
			senses("MOVE.09", "MOVE.07", "TRANSFER.04", "TRANSFER.13"),
		enC("C.TAKE.EN.02", "TAKE", "SUBJ V OBJ from LOCATION", "take", 0.70).
			senses("MOVE.09").note("taken from a place rather than from a person"),
		jaC("C.TAKE.JA.01", "TAKE", "SUBJ OBJ を V", "取る", 0.86).
			senses("MOVE.09", "MOVE.07", "TRANSFER.13"),
		jaC("C.TAKE.JA.02", "TAKE", "SUBJ OBJ を SOURCE から V", "取る", 0.74).
			senses("MOVE.09", "TRANSFER.13").note("〜を〜から取る"),
	)

	// --- SAY ---------------------------------------------------------------
	out = append(out,
		enC("C.SAY.EN.01", "SAY", "SUBJ V OBJ", "say", 0.90).
			senses("COMMUNICATE.01", "COMMUNICATE.10", "COMMUNICATE.11"),
		enC("C.SAY.EN.02", "SAY", "SUBJ V OBJ to RECIPIENT", "say", 0.80).
			senses("COMMUNICATE.01").note("addressed speech act"),
		enC("C.SAY.EN.03", "SAY", "SUBJ V OBJ", "state", 0.55).
			senses("COMMUNICATE.01").reg(RegisterTechnical).note("technical register prefers state"),
		jaC("C.SAY.JA.01", "SAY", "SUBJ OBJ を V", "言う", 0.90).
			senses("COMMUNICATE.01", "COMMUNICATE.10", "COMMUNICATE.11"),
		jaC("C.SAY.JA.02", "SAY", "SUBJ OBJ と quote V", "言う", 0.86).
			senses("COMMUNICATE.01").comp("quot").note("quoted speech: 〜と言った"),
		jaC("C.SAY.JA.03", "SAY", "SUBJ OBJ を V", "申す", 0.80).
			senses("COMMUNICATE.01").hon().req("honorific", "1"),
	)

	// --- TELL --------------------------------------------------------------
	out = append(out,
		enC("C.TELL.EN.01", "TELL", "SUBJ V RECIPIENT OBJ", "tell", 0.92).
			senses("COMMUNICATE.02").
			note("tell takes an indirect object before the direct object"),
		enC("C.TELL.EN.02", "TELL", "SUBJ V RECIPIENT about OBJ", "tell", 0.60).
			senses("COMMUNICATE.02").note("tell somebody about something"),
		jaC("C.TELL.JA.01", "TELL", "SUBJ OBJ を RECIPIENT に V", "伝える", 0.90).
			senses("COMMUNICATE.02"),
		jaC("C.TELL.JA.02", "TELL", "SUBJ OBJ と quote V", "伝える", 0.78).
			senses("COMMUNICATE.02").comp("quot").note("〜と伝えた"),
	)

	// --- ASK ---------------------------------------------------------------
	out = append(out,
		enC("C.ASK.EN.01", "ASK", "SUBJ V RECIPIENT OBJ", "ask", 0.90).
			senses("COMMUNICATE.03"),
		enC("C.ASK.EN.02", "ASK", "SUBJ V RECIPIENT for OBJ", "ask", 0.70).
			senses("COMMUNICATE.03").
			req("speech_act", "request").note("request rather than question"),
		jaC("C.ASK.JA.01", "ASK", "SUBJ OBJ を RECIPIENT に V", "尋ねる", 0.90).
			senses("COMMUNICATE.03"),
		jaC("C.ASK.JA.02", "ASK", "SUBJ OBJ を RECIPIENT に V", "聞く", 0.84).
			senses("COMMUNICATE.03"),
		jaC("C.ASK.JA.03", "ASK", "SUBJ OBJ を RECIPIENT に V", "頼む", 0.78).
			senses("COMMUNICATE.03").req("speech_act", "request").note("〜を頼む"),
	)

	// --- ANSWER ------------------------------------------------------------
	out = append(out,
		enC("C.ANSWER.EN.01", "ANSWER", "SUBJ V to RECIPIENT", "answer", 0.88).
			senses("COMMUNICATE.04"),
		enC("C.ANSWER.EN.02", "ANSWER", "SUBJ V OBJ", "answer", 0.80).
			senses("COMMUNICATE.04"),
		jaC("C.ANSWER.JA.01", "ANSWER", "SUBJ RECIPIENT に V", "答える", 0.90).
			senses("COMMUNICATE.04"),
	)

	// --- SPEAK -------------------------------------------------------------
	out = append(out,
		enC("C.SPEAK.EN.01", "SPEAK", "SUBJ V with COMITATIVE", "speak", 0.84).
			senses("COMMUNICATE.06").note("speak with somebody"),
		enC("C.SPEAK.EN.02", "SPEAK", "SUBJ V about OBJ", "talk", 0.76).
			senses("COMMUNICATE.06"),
		jaC("C.SPEAK.JA.01", "SPEAK", "SUBJ COMITATIVE と V", "話す", 0.86).
			senses("COMMUNICATE.06"),
	)

	// --- MOTION ------------------------------------------------------------
	out = append(out,
		enC("C.MOVE.EN.01", "MOVE", "SUBJ V toward GOAL", "come", 0.92).
			senses("MOVE.02").req("direction", "toward").note("motion towards the speaker"),
		enC("C.MOVE.EN.02", "MOVE", "SUBJ V away from SOURCE", "go", 0.92).
			senses("MOVE.01").req("direction", "away").note("motion away from the speaker"),
		enC("C.MOVE.EN.06", "MOVE", "SUBJ V back to SOURCE", "return", 0.90).
			senses("MOVE.03").req("direction", "back").note("return to a previous location"),
		enC("C.MOVE.EN.03", "MOVE", "SUBJ V to LOCATION", "move", 0.84).
			senses("MOVE").note("motion without a deictic anchor"),
		enC("C.MOVE.EN.07", "MOVE", "SUBJ V to LOCATION", "move", 0.86).
			senses("MOVE").note("bare motion to a place"),
		enC("C.MOVE.EN.04", "MOVE", "SUBJ V from SOURCE to GOAL", "go", 0.82).
			senses("MOVE").note("motion from a source to a goal"),
		enC("C.MOVE.EN.05", "MOVE", "SUBJ V to GOAL", "walk", 0.66).
			senses("MOVE.06").req("tense", "present").note("deictic walk towards the speaker"),
		jaC("C.MOVE.JA.01", "MOVE", "SUBJ GOAL に V", "来る", 0.92).
			senses("MOVE.02").req("direction", "toward").note("motion towards a goal"),
		jaC("C.MOVE.JA.02", "MOVE", "SUBJ SOURCE から GOAL に V", "行く", 0.90).
			senses("MOVE"),
		jaC("C.MOVE.JA.03", "MOVE", "SUBJ SOURCE から V", "行く", 0.80).
			senses("MOVE.01", "MOVE.05").req("direction", "away").note("departure only"),
		jaC("C.MOVE.JA.04", "MOVE", "SUBJ GOAL に V", "いらっしゃる", 0.86).
			senses("MOVE.02").hon().req("honorific", "1"),
	)

	// --- ARRIVE / LEAVE ----------------------------------------------------
	out = append(out,
		enC("C.ARRIVE.EN.01", "ARRIVE", "SUBJ V at LOCATION", "arrive", 0.90).
			senses("MOVE.04"),
		enC("C.ARRIVE.EN.02", "ARRIVE", "SUBJ V in LOCATION", "arrive", 0.70).
			senses("MOVE.04").note("arrival into a bounded place"),
		jaC("C.ARRIVE.JA.01", "ARRIVE", "SUBJ LOCATION に V", "着く", 0.90).
			senses("MOVE.04"),
		jaC("C.ARRIVE.JA.02", "ARRIVE", "SUBJ LOCATION に V", "到着する", 0.72).
			senses("MOVE.04").reg(RegisterTechnical).pol("masu"),
		enC("C.LEAVE.EN.01", "LEAVE", "SUBJ V LOCATION", "leave", 0.88).
			senses("MOVE.05", "MOVE.13"),
		enC("C.LEAVE.EN.02", "LEAVE", "SUBJ V", "leave", 0.74).
			senses("MOVE.05", "MOVE.13").note("intransitive leave"),
		jaC("C.LEAVE.JA.01", "LEAVE", "SUBJ LOCATION を V", "出る", 0.90).
			senses("MOVE.05", "MOVE.13"),
		jaC("C.LEAVE.JA.02", "LEAVE", "SUBJ V", "去る", 0.80).
			senses("MOVE.05", "MOVE.13"),
	)

	// --- MEET / VISIT ------------------------------------------------------
	out = append(out,
		enC("C.MEET.EN.01", "MEET", "SUBJ V OBJ", "meet", 0.92).
			senses("MEET.01"),
		enC("C.MEET.EN.02", "MEET", "SUBJ V OBJ at LOCATION", "meet", 0.70).
			senses("MEET.01").note("arranged encounter with a place"),
		enC("C.MEET.EN.03", "MEET", "SUBJ V OBJ", "visit", 0.60).
			senses("MEET.02").reg(RegisterFormal).note("a visit rather than an encounter"),
		jaC("C.MEET.JA.01", "MEET", "SUBJ OBJ に V", "会う", 0.90).
			senses("MEET.01"),
		jaC("C.MEET.JA.02", "MEET", "SUBJ OBJ を V", "会う", 0.78).
			senses("MEET.01", "MEET.02").note("contact with an organization"),
		jaC("C.MEET.JA.03", "MEET", "SUBJ OBJ に V", "お会いする", 0.86).
			senses("MEET.01").hon().req("honorific", "1"),
	)

	// --- WANT --------------------------------------------------------------
	out = append(out,
		enC("C.WANT.EN.01", "WANT", "SUBJ V OBJ", "want", 0.90).senses("MENTAL.07"),
		enC("C.WANT.EN.02", "WANT", "SUBJ V to OBJ", "want", 0.84).
			senses("MENTAL.07").
			join("to").comp("to").note("controlled predicate: want to eat"),
		enC("C.WANT.EN.03", "WANT", "SUBJ V OBJ", "would like", 0.78).
			senses("MENTAL.07").note("more polite than want"),
		jaC("C.WANT.JA.01", "WANT", "SUBJ OBJ を V", "欲しい", 0.92).
			senses("MENTAL.07").note("desiderative adjective"),
		jaC("C.WANT.JA.02", "WANT", "SUBJ OBJ を V", "ほしい", 0.74).
			senses("MENTAL.07").pol("plain").note("plain written form of 欲しい"),
		jaC("C.WANT.JA.03", "WANT", "SUBJ OBJ を V TAIL", "", 0.88).
			senses("MENTAL.07").chain("tai").tail("たい").
			note("〜たい: the theme's verb plus たい"),
	)

	// --- INTEND / PLAN -----------------------------------------------------
	out = append(out,
		enC("C.INTEND.EN.01", "INTEND", "SUBJ V JOIN OBJ", "intend", 0.88).
			senses("MENTAL.08", "MENTAL.09", "MENTAL.10").
			join("to").comp("to").note("intends to Y"),
		enC("C.INTEND.EN.02", "INTEND", "SUBJ V JOIN OBJ", "mean", 0.78).
			senses("MENTAL.10").
			join("to").comp("to").note("means to Y"),
		enC("C.INTEND.EN.03", "INTEND", "SUBJ V JOIN OBJ", "plan", 0.82).
			senses("MENTAL.09").
			join("to").comp("to").note("plans to Y"),
		enC("C.INTEND.EN.04", "INTEND", "SUBJ V OBJ", "intend", 0.70).
			senses("MENTAL.08").note("intention towards an entity rather than an event"),
		jaC("C.INTEND.JA.01", "INTEND", "SUBJ OBJ を V TAIL", "", 0.90).
			senses("MENTAL.08", "MENTAL.09", "MENTAL.10").
			chain("dic").tail("つもりだ").note("〜するつもりだ"),
		jaC("C.INTEND.JA.02", "INTEND", "SUBJ OBJ を V TAIL", "", 0.78).
			senses("MENTAL.08", "MENTAL.09", "MENTAL.10").
			chain("dic").tail("意図がある").note("〜する意図がある"),
		jaC("C.INTEND.JA.03", "INTEND", "SUBJ OBJ を V TAIL", "", 0.62).
			senses("MENTAL.09").
			chain("dic").tail("予定だ").reg(RegisterTechnical).note("〜する予定だ"),
		jaC("C.INTEND.JA.04", "INTEND", "SUBJ OBJ を V TAIL", "", 0.84).
			senses("MENTAL.08").
			chain("dic").tail("つもりだ").pol("masu").note("〜するつもりです"),
	)

	// --- BE / EXIST / NAMED -----------------------------------------------
	out = append(out,
		enC("C.BE.EN.01", "BE", "SUBJ V OBJ", "be", 0.70).
			senses("EXIST.01", "EXIST.03").
			note("copular predication"),
		jaC("C.BE.JA.01", "BE", "SUBJ が V", "いる", 0.86).
			senses("EXIST").note("animate existence"),
		jaC("C.BE.JA.02", "BE", "SUBJ が LOCATION に V", "いる", 0.90).
			senses("BE").note("existence at a place"),
		jaC("C.BE.JA.03", "BE", "SUBJ が V", "ある", 0.84).
			senses("BE").subj(jlir.RolePatient).note("inanimate existence, patient as subject"),
		enC("C.EXIST.EN.01", "EXIST", "V OBJ", "there is", 0.90).
			senses("EXIST").subj(jlir.RolePatient).note("existential there"),
		jaC("C.EXIST.JA.01", "EXIST", "SUBJ が V", "ある", 0.92).
			senses("EXIST").subj(jlir.RolePatient),
		enC("C.NAMED.EN.01", "NAMED", "SUBJ V OBJ", "be called", 0.94).
			senses("COMMUNICATE.09", "EXIST.01").note("naming: X is called Y"),
		enC("C.NAMED.EN.02", "NAMED", "SUBJ V OBJ", "be named", 0.78).
			senses("COMMUNICATE.09", "EXIST.01").reg(RegisterFormal),
		jaC("C.NAMED.JA.03", "NAMED", "SUBJ OBJ と TAIL", "", 0.84).
			senses("COMMUNICATE.09").tail("いう名前だ").note("〜という名前だ"),
		jaC("C.NAMED.JA.02", "NAMED", "SUBJ OBJ V", "だ", 0.70).
			senses("EXIST.01").pol("da").note("copular identification"),
	)

	// --- BECOME ------------------------------------------------------------
	out = append(out,
		enC("C.BECOME.EN.01", "BECOME", "SUBJ V OBJ", "become", 0.92).senses("CHANGE.06"),
		jaC("C.BECOME.JA.01", "BECOME", "SUBJ OBJ に V", "なる", 0.92).senses("CHANGE.06"),
	)

	// --- HAVE / POSSESS ---------------------------------------------------
	out = append(out,
		enC("C.HAVE.EN.01", "HAVE", "SUBJ V OBJ", "have", 0.92).
			senses("HAVE"),
		enC("C.HAVE.EN.02", "HAVE", "SUBJ V got OBJ", "have", 0.70).
			senses("HAVE.01").reg(RegisterCasual).note("have got, spoken"),
		enC("C.HAVE.EN.03", "HAVE", "SUBJ V OBJ", "own", 0.84).
			senses("HAVE.02", "HAVE.03"),
		jaC("C.HAVE.JA.01", "HAVE", "SUBJ OBJ を V", "持つ", 0.92).
			senses("HAVE"),
		jaC("C.HAVE.JA.02", "HAVE", "SUBJ の OBJ を V", "持つ", 0.84).
			senses("HAVE").note("〜の〜: nominal genitive possession"),
		jaC("C.HAVE.JA.03", "HAVE", "SUBJ OBJ を V", "お持ちする", 0.80).
			senses("HAVE").hon().req("honorific", "1"),
	)

	// --- ABLE / MUST / SHOULD / MAY --------------------------------------
	out = append(out,
		enC("C.ABLE.EN.01", "ABLE", "SUBJ V JOIN OBJ", "be able", 0.84).
			senses("MODAL.04", "MODAL.06").
			join("to").comp("to").note("is able to eat"),
		enC("C.ABLE.EN.02", "ABLE", "SUBJ V OBJ", "can", 0.90).
			senses("MODAL.04").note("dynamic modal"),
		jaC("C.ABLE.JA.01", "ABLE", "SUBJ OBJ を V", "", 0.92).
			senses("MODAL.04", "MODAL.06").
			chain("potential").note("potential: 〜られる, from the theme's verb"),
		jaC("C.ABLE.JA.02", "ABLE", "SUBJ OBJ が V", "できる", 0.84).
			senses("MODAL.06").note("〜ができる as a lexical predicate"),
		enC("C.MUST.EN.01", "MUST", "SUBJ V OBJ", "must", 0.92).
			senses("MODAL.01", "MODAL.07"),
		enC("C.MUST.EN.02", "MUST", "SUBJ V OBJ", "have to", 0.78).
			senses("MODAL.01").note("external obligation"),
		jaC("C.MUST.JA.01", "MUST", "SUBJ OBJ を V TAIL", "", 0.92).
			senses("MODAL.01").
			chain("nai_n").tail("ければならない").note("〜なければならない"),
		jaC("C.MUST.JA.02", "MUST", "SUBJ OBJ を V TAIL", "", 0.62).
			senses("MODAL.07").
			reg(RegisterCasual).chain("ta").tail("てはいけない").note("〜てはいけない"),
		enC("C.SHOULD.EN.01", "SHOULD", "SUBJ V JOIN OBJ", "should", 0.90).
			senses("MODAL.02").
			join("to").comp("to"),
		enC("C.SHOULD.EN.02", "SHOULD", "SUBJ V OBJ", "ought to", 0.70).
			senses("MODAL.02").reg(RegisterFormal),
		jaC("C.SHOULD.JA.01", "SHOULD", "SUBJ OBJ を V TAIL", "", 0.90).
			senses("MODAL.02").
			chain("tai").tail("るべきだ").note("〜るべきだ"),
		jaC("C.SHOULD.JA.02", "SHOULD", "SUBJ OBJ を V TAIL", "", 0.72).
			senses("MODAL.02").
			chain("ta").tail("たほうがいい").note("〜たほうがいい"),
		enC("C.MAY.EN.01", "MAY", "SUBJ V OBJ", "may", 0.90).
			senses("MODAL.03", "MODAL.05"),
		enC("C.MAY.EN.02", "MAY", "SUBJ V OBJ", "be allowed to", 0.74).
			senses("MODAL.05").comp("to").note("permission"),
		jaC("C.MAY.JA.01", "MAY", "SUBJ OBJ を V TAIL", "", 0.84).
			senses("MODAL.03", "MODAL.05").
			chain("ta").tail("てもいい").note("〜てもいい"),
	)

	// --- EAT / DRINK ------------------------------------------------------
	out = append(out,
		enC("C.EAT.EN.01", "EAT", "SUBJ V OBJ", "eat", 0.94).
			senses("CONSUME.01").req("ingestible", "solid").note("solid food only: a liquid theme is DRINK"),
		enC("C.EAT.EN.02", "EAT", "SUBJ V OBJ", "have", 0.66).
			senses("CONSUME.01").req("ingestible", "solid").
			reg(RegisterCasual).note("have something to eat"),
		jaC("C.EAT.JA.01", "EAT", "SUBJ OBJ を V", "食べる", 0.94).
			senses("CONSUME.01").req("ingestible", "solid").note("solid food only: a liquid theme is 飲む"),
		jaC("C.EAT.JA.02", "EAT", "SUBJ OBJ を V", "いただく", 0.84).
			senses("CONSUME.01").req("ingestible", "solid").hon().req("honorific", "1"),
		enC("C.DRINK.EN.01", "DRINK", "SUBJ V OBJ", "drink", 0.94).
			senses("CONSUME.02").req("ingestible", "liquid").note("liquid only: a solid theme is EAT"),
		jaC("C.DRINK.JA.01", "DRINK", "SUBJ OBJ を V", "飲む", 0.94).
			senses("CONSUME.02").req("ingestible", "liquid").note("liquid only: a solid theme is 食べる"),
		jaC("C.DRINK.JA.02", "DRINK", "SUBJ OBJ を V", "いただく", 0.84).
			senses("CONSUME.02").req("ingestible", "liquid").hon().req("honorific", "1"),
	)

	// --- SEE / HEAR -------------------------------------------------------
	out = append(out,
		enC("C.SEE.EN.01", "SEE", "SUBJ V OBJ", "see", 0.92).senses("PERCEIVE.01", "PERCEIVE.06"),
		enC("C.SEE.EN.02", "SEE", "SUBJ V OBJ", "watch", 0.72).
			senses("PERCEIVE.05").note("deliberate watching"),
		enC("C.SEE.EN.03", "SEE", "SUBJ V OBJ", "look at", 0.70).senses("PERCEIVE.02"),
		jaC("C.SEE.JA.01", "SEE", "SUBJ OBJ を V", "見る", 0.94).senses("PERCEIVE.01"),
		jaC("C.SEE.JA.02", "SEE", "SUBJ OBJ を V", "観る", 0.70).
			senses("PERCEIVE.05").note("watching a performance"),
		jaC("C.SEE.JA.03", "SEE", "SUBJ OBJ を V", "ご覧になる", 0.84).
			senses("PERCEIVE.01").hon().req("honorific", "1"),
		enC("C.HEAR.EN.01", "HEAR", "SUBJ V OBJ", "hear", 0.92).senses("PERCEIVE.03"),
		enC("C.HEAR.EN.02", "HEAR", "SUBJ V OBJ from SOURCE", "hear", 0.78).
			senses("PERCEIVE.03", "PERCEIVE.04"),
		jaC("C.HEAR.JA.01", "HEAR", "SUBJ OBJ を V", "聞く", 0.90).senses("PERCEIVE.03"),
		jaC("C.HEAR.JA.02", "HEAR", "SUBJ OBJ が V", "聞こえる", 0.82).
			senses("PERCEIVE.03").note("the sound reaches the experiencer"),
	)

	// --- KNOW / THINK / BELIEVE -------------------------------------------
	out = append(out,
		enC("C.KNOW.EN.01", "KNOW", "SUBJ V OBJ", "know", 0.92).
			senses("MENTAL.02", "MENTAL.06", "MENTAL.11"),
		enC("C.KNOW.EN.02", "KNOW", "SUBJ V JOIN OBJ", "know", 0.84).
			senses("MENTAL.02").
			join("that").comp("that").note("know that he said"),
		jaC("C.KNOW.JA.01", "KNOW", "SUBJ OBJ を V", "知る", 0.92).
			senses("MENTAL.02", "MENTAL.06", "MENTAL.11"),
		jaC("C.KNOW.JA.02", "KNOW", "SUBJ OBJ と quote V", "知る", 0.74).
			senses("MENTAL.02").comp("quot").note("〜と知っている"),
		enC("C.THINK.EN.01", "THINK", "SUBJ V OBJ", "think", 0.86).
			senses("MENTAL.01", "MENTAL.04", "MENTAL.13"),
		enC("C.THINK.EN.02", "THINK", "SUBJ V JOIN OBJ", "think", 0.84).
			senses("MENTAL.01", "MENTAL.04").
			join("that").comp("that"),
		enC("C.THINK.EN.03", "THINK", "SUBJ V about OBJ", "think", 0.66).
			senses("MENTAL.12", "MENTAL.13"),
		jaC("C.THINK.JA.01", "THINK", "SUBJ OBJ と quote V", "思う", 0.90).
			senses("MENTAL.01", "MENTAL.04").comp("quot").note("〜と思う"),
		jaC("C.THINK.JA.02", "THINK", "SUBJ OBJ と quote V", "考える", 0.78).
			senses("MENTAL.12", "MENTAL.13").comp("quot").note("〜と考える"),
		enC("C.BELIEVE.EN.01", "BELIEVE", "SUBJ V OBJ", "believe", 0.90).
			senses("MENTAL.03", "MENTAL.12"),
		enC("C.BELIEVE.EN.02", "BELIEVE", "SUBJ V JOIN OBJ", "believe", 0.78).
			senses("MENTAL.03").
			join("that").comp("that"),
		jaC("C.BELIEVE.JA.01", "BELIEVE", "SUBJ OBJ と quote V", "思う", 0.84).
			senses("MENTAL.03").comp("quot").note("〜と思う"),
	)

	// --- COMPARE ----------------------------------------------------------
	out = append(out,
		enC("C.CMP.EN.01", "COMPARE", "SUBJ V OBJ with STIMULUS", "compare", 0.92).
			senses("COMPARE.01", "COMPARE.03", "COMPARE.04"),
		enC("C.CMP.EN.02", "COMPARE", "SUBJ V OBJ to STIMULUS", "compare", 0.84).
			senses("COMPARE.02", "COMPARE.05").
			note("comparison against a standard"),
		enC("C.CMP.EN.03", "COMPARE", "SUBJ V OBJ", "be like", 0.66).
			senses("COMPARE.07", "COMPARE.08").
			note("similarity without an explicit standard"),
		jaC("C.CMP.JA.01", "COMPARE", "SUBJ OBJ を STIMULUS と V", "比べる", 0.90).
			senses("COMPARE.01", "COMPARE.02", "COMPARE.03"),
		jaC("C.CMP.JA.02", "COMPARE", "SUBJ OBJ に V", "似ている", 0.84).
			senses("COMPARE.07").note("〜に似ている"),
		jaC("C.CMP.JA.03", "COMPARE", "SUBJ OBJ と V", "同じ", 0.76).
			senses("COMPARE.05", "COMPARE.08").note("〜と同じ"),
	)

	// --- COPULA -----------------------------------------------------------
	out = append(out,
		enC("C.COPULA.EN.01", "COPULA", "SUBJ V OBJ", "be", 0.80).
			senses("COPULA.01", "COPULA.02"),
		enC("C.COPULA.EN.02", "COPULA", "SUBJ V OBJ", "remain", 0.50).
			senses("EXIST.05", "COPULA.01").reg(RegisterLiterary),
		// COPULA.03 (です) and COPULA.05 (でしょう) had no English
		// construction at all, so 「今日はいい天気ですね。」 resolved its
		// predicate correctly and then had nothing to build it from. The
		// ontology declares both senses and the Japanese side realizes them.
		// A copula with a complement needs a slot for it. 「今日はいい天気です」
		// puts the property in the theme role, and a frame with no theme slot
		// cannot realize the clause at all.
		enC("C.COPULA.EN.03", "COPULA", "SUBJ V OBJ", "be", 0.80).
			senses("COPULA.03").note("polite copula: です is the English plain copula"),
		enC("C.COPULA.EN.04", "COPULA", "SUBJ V OBJ", "be probably", 0.72).
			senses("COPULA.05").note("conjectural copula: でしょう asserts a probability"),
		enC("C.COPULA.EN.05", "COPULA", "SUBJ V OBJ", "will be", 0.58).
			senses("COPULA.05").note("conjectural copula read as prediction"),
		jaC("C.COPULA.JA.01", "COPULA", "SUBJ V", "だ", 0.70).
			senses("COPULA.01").pol("da").note("plain copula"),
		jaC("C.COPULA.JA.02", "COPULA", "SUBJ V", "である", 0.74).
			senses("COPULA.04").pol("da").reg(RegisterFormal).note("written copula"),
		jaC("C.COPULA.JA.03", "COPULA", "SUBJ OBJ V", "だ", 0.80).
			senses("COPULA.03").pol("da").note("〜だ / 〜です"),
	)

	// --- misc high frequency families --------------------------------------
	out = append(out,
		enC("C.LIKE.EN.01", "LIKE", "SUBJ V OBJ", "like", 0.90).
			senses("FEEL.01", "FEEL.02"),
		jaC("C.LIKE.JA.01", "LIKE", "SUBJ OBJ が V", "好き", 0.90).
			senses("FEEL.01", "FEEL.02").note("〜が好き"),
		jaC("C.LIKE.JA.02", "LIKE", "SUBJ OBJ を V", "嫌い", 0.84).
			senses("FEEL.03").note("〜を嫌い"),
		enC("C.SHOW.EN.01", "SHOW", "SUBJ V OBJ to RECIPIENT", "show", 0.92).
			senses("TRANSFER.08"),
		jaC("C.SHOW.JA.01", "SHOW", "SUBJ OBJ を RECIPIENT に V", "見せる", 0.90).
			senses("TRANSFER.08"),
		enC("C.READ.EN.01", "READ", "SUBJ V OBJ", "read", 0.92).senses("WORK.04"),
		jaC("C.READ.JA.01", "READ", "SUBJ OBJ を V", "読む", 0.92).senses("WORK.04"),
		enC("C.WRITE.EN.01", "WRITE", "SUBJ V OBJ", "write", 0.92).
			senses("COMMUNICATE.07"),
		jaC("C.WRITE.JA.01", "WRITE", "SUBJ OBJ に V", "書く", 0.92).
			senses("COMMUNICATE.07"),
		enC("C.SLEEP.EN.01", "SLEEP", "SUBJ V", "sleep", 0.92).senses("SLEEP.01"),
		jaC("C.SLEEP.JA.01", "SLEEP", "SUBJ V", "寝る", 0.92).senses("SLEEP.01"),
		enC("C.LIVE.EN.01", "LIVE", "SUBJ V in LOCATION", "live", 0.90).
			senses("EXIST.08"),
		jaC("C.LIVE.JA.01", "LIVE", "SUBJ LOCATION に V", "住む", 0.90).
			senses("EXIST.08"),
		enC("C.WORK.EN.01", "WORK", "SUBJ V", "work", 0.90).senses("WORK.01", "WORK.02"),
		jaC("C.WORK.JA.01", "WORK", "SUBJ V", "働く", 0.90).senses("WORK.01"),
		enC("C.OPEN.EN.01", "OPEN", "SUBJ V OBJ", "open", 0.90).senses("CHANGE.01"),
		jaC("C.OPEN.JA.01", "OPEN", "SUBJ OBJ を V", "開く", 0.90).senses("CHANGE.01"),
		enC("C.CLOSE.EN.01", "CLOSE", "SUBJ V OBJ", "close", 0.90).senses("CHANGE.01"),
		jaC("C.CLOSE.JA.01", "CLOSE", "SUBJ OBJ を V", "閉める", 0.90).senses("CHANGE.01"),
		enC("C.START.EN.01", "START", "SUBJ V OBJ", "start", 0.90).senses("TIME.04"),
		jaC("C.START.JA.01", "START", "SUBJ OBJ を V", "始める", 0.90).senses("TIME.04"),
		enC("C.STOP.EN.01", "STOP", "SUBJ V OBJ", "stop", 0.90).senses("TIME.05"),
		jaC("C.STOP.JA.01", "STOP", "SUBJ OBJ を V", "やめる", 0.90).senses("TIME.05"),
		enC("C.SEND.EN.01", "SEND", "SUBJ V OBJ to RECIPIENT", "send", 0.92).
			senses("TRANSFER.02"),
		jaC("C.SEND.JA.01", "SEND", "SUBJ OBJ を RECIPIENT に V", "送る", 0.92).
			senses("TRANSFER.02"),
		enC("C.BUY.EN.01", "BUY", "SUBJ V OBJ from SOURCE", "buy", 0.92).
			senses("TRANSFER.05"),
		jaC("C.BUY.JA.01", "BUY", "SUBJ OBJ を SOURCE から V", "買う", 0.92).
			senses("TRANSFER.05"),
		enC("C.SELL.EN.01", "SELL", "SUBJ V OBJ to RECIPIENT", "sell", 0.92).
			senses("TRANSFER.06").
			note("selling keeps the buyer as recipient, never the reverse"),
		jaC("C.SELL.JA.01", "SELL", "SUBJ OBJ を RECIPIENT に V", "売る", 0.92).
			senses("TRANSFER.06"),
		jaC("C.SELL.JA.02", "SELL", "SUBJ OBJ を RECIPIENT に V", "お売りする", 0.84).
			senses("TRANSFER.06").hon().req("honorific", "1"),
	)

	return out
}

// enPrep returns the preposition English conventionally uses for a role. The
// realizer overrides it with a preposition written into the pattern.
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
