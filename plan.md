JEV-Trans

A Bidirectional Constraint-Based Translation System for Japanese and English

Status: Architecture Concept / Draft 0.1
Initial language pair: Japanese ↔ English
Primary objective: 最大限の意味保存・説明可能性・非幻覚性を持つ翻訳
Secondary objective: 自由生成型LLMを翻訳生成器として使用しない

---

1. 概要

JEV-Transは、一般的なNeural Machine Translationとは根本的に異なる翻訳システムである。

通常のLLM/NMTは、

"source text → neural model → target text"

という直接変換を行う。

JEV-Transではこれを行わない。

基本パイプラインは、

"source language → linguistic analysis → semantic representation → target-language planning → grammatical realization → semantic verification"

である。

モデルの役割も文章生成ではない。

Jevのような有限候補判定モデルを、構文解析・語義曖昧性・照応・談話解釈・構文選択・最終候補選択などに使用する。

したがって、

«モデルは翻訳を書かない。翻訳に必要な曖昧性を解く。»

翻訳文そのものは、

- grammar
- lexicon
- ontology
- construction library
- semantic representation
- constraint solver
- morphological realizer

によって構成される。

---

2. 設計目標

JEV-Transにおける「完全な翻訳」とは、単にBLEUや自然さが高いことではない。

以下を満たす翻訳を理想とする。

2.1 Semantic Fidelity

原文中で確定している意味を保存する。

以下を含む。

- predicate
- semantic roles
- event structure
- polarity
- tense
- aspect
- modality
- quantification
- scope
- comparison
- condition
- causation
- intention
- presupposition
- referents

---

3. Preservation と Realization を分離する

このシステムで最も重要な原則である。

例えば、

«I saw him.»

の "I" は翻訳先でも必ず「私」として文字列化しなければならないわけではない。

日本語なら、

«彼を見た。»

でもspeaker referentは保存されている。

したがって、

意味保存

と

表層表現保存

は別物である。

内部では、

"entity = speaker"

を維持する。

日本語生成時には、

"surface realization = ZERO"

を選択できる。

つまり、

"source overt pronoun → semantic entity → target zero pronoun"

は正常な翻訳である。

---

4. 非発明原則

翻訳システムは、原文または確実な文脈に存在しない情報を勝手に追加してはいけない。

例：

«帰った。»

から、

«She went home.»

を無条件に生成することは禁止する。

なぜなら、

"gender = female"

という情報が原文に存在しないからである。

内部的には、

"agent ∈ possible discourse entities"

という未確定状態として保持する。

必要なら、

- They went home.
- Yamada went home.
- He went home.
- She went home.

などを候補として作れるが、原文だけで決まらないものは確定してはならない。

---

5. 不可逆翻訳を明示的に扱う

日本語と英語は同じ情報を文法化していない。

例えば日本語の、

«先生がいらっしゃいました。»

には話者から人物への敬意が形態・語彙的に表現されている。

英語の、

«The teacher arrived.»

にはその情報を完全に符号化する自然な対応形が存在しない。

そのためJEV-Transでは、

"loss = 0"

を無条件に要求しない。

代わりに、

"unrealizable source feature"

を記録する。

例：

"HONORIFIC(subject=teacher, level=respectful)"

が英語で直接表現不能なら、

"preserved internally = true"

"surface realization = unavailable"

"translation loss = pragmatic"

と記録する。

つまりシステム自身が、

«何が翻訳できなかったか»

を認識する。

---

6. 全体アーキテクチャ

                       DOCUMENT STATE
              ┌───────────────────────────────┐
              │ entities / events / speakers │
              │ timeline / discourse / style │
              │ world knowledge / terminology│
              └───────────────┬───────────────┘
                              │
                        SOURCE TEXT
                              │
                              ▼
                 ┌─────────────────────┐
                 │ INPUT NORMALIZATION │
                 └──────────┬──────────┘
                            │
                            ▼
               ┌──────────────────────────┐
               │ MORPHOLOGICAL LATTICE   │
               └────────────┬─────────────┘
                            │
                            ▼
              ┌────────────────────────────┐
              │ PACKED SYNTACTIC FOREST    │
              │ HPSG + dependency + rules  │
              └─────────────┬──────────────┘
                            │
                            ▼
              ┌────────────────────────────┐
              │ SOURCE SEMANTIC FOREST     │
              └─────────────┬──────────────┘
                            │
                            ▼
                  ┌───────────────────┐
                  │     JLIR CORE     │
                  │                   │
                  │ semantics         │
                  │ discourse         │
                  │ pragmatics        │
                  │ uncertainty       │
                  │ provenance        │
                  └────────┬──────────┘
                           │
                           ▼
                   JEV DECISION GRAPH
                           │
                           ▼
                CONSTRAINED JLIR STATE
                           │
                           ▼
             ┌──────────────────────────┐
             │ TARGET PROJECTION ENGINE │
             └────────────┬─────────────┘
                          │
                          ▼
               TARGET MESSAGE PLANNER
                          │
                          ▼
              CONSTRUCTION SELECTION
                          │
                          ▼
                PACKED REALIZATION FOREST
                          │
                          ▼
                GRAMMATICAL REALIZER
                          │
                    candidate texts
                          │
                          ▼
                  TARGET RE-PARSER
                          │
                          ▼
                    TARGET JLIR
                          │
                          ▼
              ┌──────────────────────────┐
              │ SEMANTIC EQUIVALENCE     │
              │       VERIFIER           │
              └────────────┬─────────────┘
                           │
                    valid candidates
                           │
                           ▼
                    JEV RERANKER
                           │
                           ▼
                     FINAL OUTPUT

---

7. JLIR

中心となるIRを、

JLIR — Jev Lossless Interlingual Representation

と呼ぶ。

ただし完全な「language-independent interlingua」にはしない。

JLIRは、

1. Core semantics
2. Referential state
3. Temporal/event state
4. Discourse state
5. Information structure
6. Pragmatics
7. Linguistic source features
8. Uncertainty
9. Provenance

という複数層を持つ。

---

8. MRSとの関係

MRSはJLIRの重要な基礎となる。

MRSは、

- predicate
- argument
- scope
- quantification
- underspecification

を扱え、特にスコープを即座に一意化せず保持できる点が重要である。

しかし翻訳用IRとしては不足する。

JEV-Transでは、

"MRS → JLIR"

とする。

つまり、

MRSを捨てるのではなく、semantic kernelとして包む。

英語についてはERG、日本語についてはJacyという既存HPSG grammarを利用できる。

JacyはHPSG + MRSベースの日本語precision grammarであり、ERGも同様にMRSを意味表現として使用する。

---

9. JLIR Semantic Core

基本単位はentityとeventである。

例：

«太郎が花子に本を渡した。»

ENTITY e1
    type: HUMAN
    identity: TARO

ENTITY e2
    type: HUMAN
    identity: HANAKO

ENTITY e3
    type: BOOK

EVENT v1
    predicate: TRANSFER
    tense: PAST

ROLE
    giver(v1)     = e1
    recipient(v1) = e2
    theme(v1)     = e3

重要なのは言語のsurface argument structureをそのまま保存しないこと。

例えば、

"ARG1"

を最終意味層の役割として使わず、

"agent"

"experiencer"

"recipient"

"theme"

などのsemantic relationへ正規化する。

---

10. Predicate Ontology

単語を言語間で直接マッピングしない。

悪い例：

"run = 走る"

正しい設計：

RUN_01
    locomotion_on_foot

RUN_02
    operate_machine

RUN_03
    manage_organization

RUN_04
    continue_functioning

RUN_05
    flow_liquid

英語surface：

run → {RUN_01, RUN_02, RUN_03, RUN_04, RUN_05 ...}

日本語surface：

走る      → RUN_01
動かす    → RUN_02
運営する  → RUN_03
稼働する  → RUN_04
流れる    → RUN_05

翻訳は、

"word → meaning → word"

で行う。

---

11. Predicateは過度に細分化しない

OntologyをWordNet型の巨大なsense dictionaryにしすぎると破綻する。

したがって、

"semantic primitive + constraints"

を優先する。

例：

TRANSFER
    agent
    source
    recipient
    theme

に、

manner
physicality
ownership_change
intentionality

などをfeatureとして持たせる。

これにより近義語を完全に別predicateとして扱う必要を減らす。

---

12. Event Representation

Eventには最低限以下を保持する。

predicate
participants

tense
aspect
perfect
progressive

polarity

modality

mood

causation

intentionality

evidentiality

event boundaries

temporal relation

例：

«食べてしまった。»

を単なる、

"EAT + PAST"

にしない。

EVENT
    predicate = EAT
    tense = past
    completion = completed

PRAGMATIC/EVALUATIVE
    speaker_attitude = context-dependent

「てしまう」の完了・遺憾などを分離する。

---

13. Scope Graph

以下のような文：

«Everyone didn't leave.»

ではscopeが重要である。

NOT > ALL

なのか、

ALL > NOT

なのかで意味が変わる。

JLIRではscopeをgraphとして保持する。

曖昧なら、

scope ∈ {
    NOT > ALL,
    ALL > NOT
}

として決定を延期する。

これはMRSのunderspecificationを引き継ぐ。

---

14. Referential Layer

JP↔ENでは極めて重要。

各entityについて、

entity_id
semantic_type

number
gender
animacy
person

speaker_relation

discourse_salience

mention_history

aliases

possible_referents

を管理する。

---

15. Zero Anaphora

日本語ではzero pronounをfirst-class objectとして扱う。

例：

«太郎は花子に会った。嬉しそうだった。»

第2文のsubjectを最初から太郎に固定しない。

ZERO z1

candidate_ref(z1):
    Taro
    Hanako
    speaker
    unknown

Jevが文脈を評価し、

posterior distributionを更新する。

重要なのは、

winner-takes-allにしすぎないこと。

例えば、

Taro   0.56
Hanako 0.43
other  0.01

なら曖昧性を保持する。

---

16. UncertaintyはJLIRの一級データ

内部状態は単一graphではなく、

Constraint Graph + Probability Distribution

である。

例えば、

speaker(x) = UNKNOWN

x ∈ {Taro, Hanako}

あるいは、

P(x=Taro)=0.62
P(x=Hanako)=0.38

とする。

翻訳先が曖昧性をそのまま表現できるなら、無理に解決しない。

これは非常に重要である。

---

17. Ambiguity Preservation Principle

翻訳先にも同じ曖昧性を自然に保存できる場合、

曖昧性は解消しない方が正しい。

例：

«あの人»

を文脈なしで、

"he"

や

"she"

にしない。

自然なら、

"they"

"that person"

などを使用する。

つまりJevの目的は、

«全ての曖昧性を解決する»

ことではない。

目的は、

«翻訳に必要な曖昧性だけを、必要な時だけ解決する»

ことである。

---

18. Pragmatic Layer

ここが従来MRSから大幅に拡張する領域である。

例：

speech_style

formality

politeness

speaker_age_style

social_distance

respect

humility

empathy

assertiveness

certainty

sarcasm

irony

emotional_tone

sentence_final_attitude

日本語では、

- です / ます
- だ / である
- ね
- よ
- ぞ
- ぜ
- わ
- かな
- かも
- でしょう
- お〜になる
- 〜ていただく

などが重要である。

これらを単なるsurface tokenとして処理しない。

---

19. Information Structure

日本語の、

"は"

と、

"が"

を英語のsubjectへ単純変換しない。

JLIRでは、

topic
focus
contrast
given/new
background

を別途保持する。

例えば、

«太郎は来た。»

では、

"topic(Taro)"

が重要であり、

単なる、

"subject(Taro)"

とは異なる。

---

20. Source Linguistic Features

完全に言語中立なIRだけでは情報が落ちる。

そのためsource-specific featuresも保持する。

例：

JP:
    honorific construction used
    topic marker は
    sentence-final よ
    explicit zero subject

これらはcore semanticsではない。

しかし翻訳品質判断には必要になる。

---

21. Provenance

JLIR内のすべての重要featureには出典を持たせる。

feature:
    gender = female

source:
    token = "she"

confidence:
    1.0

origin:
    lexical

あるいは、

referent = Hanako

origin:
    discourse inference

decision:
    JEV-532

confidence:
    0.87

これにより、

どこからその情報が生まれたのか

を追跡できる。

---

22. Hallucination Detection

target側にfeatureが存在するのに、そのfeatureへのprovenance pathが存在しない場合、

"UNSUPPORTED_INFORMATION"

として扱う。

例えば、

«先生が来た。»

→

«The female teacher came.»

なら、

"gender=female"

にはsource provenanceがない。

したがってrejectする。

---

23. Source Analysisは単一parserに依存しない

理想的には、

HPSG
dependency parser
morphological analyzer
named entity recognizer
idiom detector

など複数解析を使用する。

結果は単一treeにせず、

Packed Analysis Forest

へ格納する。

Jacyは日本語HPSG/MRS grammarとして有用であり、ERGは英語側のprecision grammarとして利用できる。

---

24. Parser Failureを許容する

precision grammarのみでは現実の文章を100%解析できない。

そのため解析層は二段構えにする。

Tier 1
precision grammar

Tier 2
robust parser

precision parseが成功すればそれを優先する。

失敗した部分のみ、

robust parserから補完する。

最終的にはJLIRへ統合する。

---

25. Unknown Construction Handling

未知表現を即座に自由生成モデルへ投げない。

以下の順で処理する。

1. morphological decomposition
2. known construction composition
3. idiom/phrase database
4. corpus-derived construction lookup
5. semantic approximation
6. unresolved node

最後まで分からなければ、

"UNKNOWN_SEMANTIC_UNIT"

として保持する。

誤った意味を勝手に作るより正しい。

---

26. Construction Grammar Layer

生成の中心を単語ではなくconstructionに置く。

例えば、

INTEND(agent,event)

に対して英語では、

X intends to Y
X means to Y
X plans to Y

日本語では、

XはYするつもりだ
XはYしようと思っている
XはYする意図がある

などを登録する。

各constructionには、

semantic requirements
syntactic requirements
register
frequency
naturalness
discourse conditions
prosody/style

を付与する。

---

27. LexiconとConstructionを連続体として扱う

idiomは単語と文法の中間に存在する。

例：

«kick the bucket»

を、

"KICK + BUCKET"

として解析してはいけない場合がある。

したがって辞書構造は、

token
multiword expression
construction
idiom
semi-productive pattern

を同一framework上で扱えるようにする。

---

28. Document State

翻訳単位をsentenceに限定しない。

Document Context Storeを持つ。

entities
entity aliases
speaker
listener
social relations

event history
timeline

topic stack
focus stack

terminology

style profile

previous translation decisions

を保持する。

これにより、

同じ人物の呼称、

同じtechnical term、

pronoun、

register

をdocument全体で統一できる。

---

29. Entity Identity

例えば、

«山田教授
山田さん
教授
彼
山田»

を別entityとして扱わない。

Document State内に、

ENTITY E17

aliases:
    山田教授
    山田さん
    山田
    professor Yamada

として統一する。

---

30. Jev Decision Graph

Jev呼び出しを無秩序に行わない。

明示的なDAGとして定義する。

D0 lexical segmentation
        │
D1 syntactic ambiguities
        │
D2 predicate senses
        │
D3 semantic roles
        │
D4 coreference / zero anaphora
        │
D5 discourse interpretation
        │
D6 pragmatic interpretation
        │
D7 target projection choices
        │
D8 construction selection
        │
D9 final ranking

前段のdecisionに依存するdecisionは後段で行う。

---

31. Jevに自由形式を出力させない

禁止：

Analyze this sentence and output its meaning as JSON.

正しい：

Which referent best matches ZERO_SUBJECT_12?

A E1
B E2
C speaker
D listener
E unresolved

あるいは、

Does construction C27 preserve the source implication?
YES / NO

Jevはdecision oracleとして使う。

---

32. Jevの候補はシステム側が生成する

Jevに候補自体を考えさせない。

候補は、

- grammar
- dictionary
- ontology
- parser forest
- discourse model
- construction database

から生成する。

これによりmodel hallucinationを構造的に封じる。

---

33. Hierarchical Decision

255候補制限などを考えると、巨大ontologyを直接Choiceさせない。

例えば10,000 senseがあるなら、

semantic class
↓
subclass
↓
predicate family
↓
specific sense

と階層化する。

これによりJev呼び出しを減らせる。

---

34. Information Gain Scheduling

全decisionを解く必要はない。

翻訳結果に影響しない曖昧性はそのまま保持する。

優先度は、

"expected translation impact × uncertainty"

で決める。

例えば、

P(A)=0.51
P(B)=0.49

でもA/Bどちらでも同じ英語表現になるならJevを呼ばない。

逆に、

P(he)=0.55
P(she)=0.45

で英語pronoun選択に直接影響するなら高優先度になる。

---

35. Target Projection

JLIRから直接文章を作らない。

まずtarget languageが要求する情報を決める。

英語なら例えば、

subject realization

determiner

number

article

pronoun

word order

tense

auxiliary

agreement

など。

日本語なら、

argument omission

topic marking

case particles

politeness morphology

honorifics

sentence-final particles

word order

ellipsis

など。

---

36. Translation as Constraint Satisfaction

Target Plannerの問題は、

«正しい文章を生成する»

ではなく、

«JLIRを満たすtarget realizationを探す»

である。

形式的には、

find T
such that

Grammar(T)
∧ Semantics(T) ⊇ RequiredMeaning
∧ Contradictions(T, Source) = ∅
∧ UnsupportedInfo(T) = ∅

を解く。

自然さはその後のobjective functionである。

---

37. Hard Constraints と Soft Constraints

Hard

違反した候補はreject。

例：

wrong entity
wrong predicate
wrong polarity
wrong number
wrong tense where semantically required
wrong scope
invented gender
missing negation

Soft

候補間rankingに利用。

naturalness
register match
idiomaticity
brevity
stylistic similarity
frequency

この分離が非常に重要。

---

38. Packed Target Forest

生成候補を文字列100万個として作らない。

共有部分をDAGとして保持する。

例：

               mean to
              /
NEG INTENT -- intend to
              \
               plan to

noun phraseやpronoun variantsなども同様に共有する。

---

39. Constraint Propagation

候補生成後に全部検証するのでは遅い。

生成中にsemantic constraintsを伝播させ、

不可能なbranchを早期削除する。

例：

source:

"gender = unknown"

なら、

"he"

"she"

が不必要にgenderを確定する場合、そのbranchを生成前または生成直後に除外できる。

---

40. Semantic Re-parsing

生成されたtarget候補を必ず再解析する。

JLIR_source
    ↓ generation
target sentence
    ↓ parser
JLIR_target

そして、

"JLIR_source ↔ JLIR_target"

を比較する。

これはJEV-Transの中核的安全装置である。

---

41. Semantic Equivalence Verifier

単なるembedding similarityを使用しない。

feature単位で比較する。

entities
events
roles
polarity
scope
quantifiers
modality
temporal relations
coreference
presupposition
pragmatics

について差分を作る。

---

42. Equivalence Classes

完全一致だけを要求すると自然な翻訳を落としてしまう。

したがってsemantic equivalence ruleを持つ。

例えば、

speaker entity explicit
↔
speaker entity zero-realized

は日本語ではequivalentになり得る。

また、

already
↔
aspectual completion marker

のようなlanguage-specific equivalenceも定義する。

---

43. Translation Loss Vector

候補ごとに、

LOSS = {
    propositional,
    referential,
    temporal,
    pragmatic,
    stylistic,
    implicature
}

を算出する。

例えば、

The teacher arrived.

が日本語敬語表現から生成された場合、

propositional = 0
referential = 0
temporal = 0
pragmatic = 0.18

のようになる。

最終候補選択では、単一scoreではなくこのvectorを考慮する。

---

44. Final Reranking

Verifier通過候補だけをJevに渡す。

ここで評価するのは、

naturalness
idiomaticity
style match
register match
contextual fit

である。

つまりJevは、

意味的に間違った文章を自然さで救済できない。

semantic verifierがhard gateになる。

---

45. Candidate Dominance

candidate Aが、

- semantic loss <= B
- pragmatic loss <= B
- style loss <= B
- naturalness >= B

ならBを削除する。

Pareto dominanceで候補数を減らせる。

---

46. JP→EN固有処理

主要問題：

Zero pronouns

英語側でsubject/objectを補う必要がある。

Articles

日本語では通常明示されない。

"a / the / zero"

をdiscourse stateから判断する。

Number

日本語で曖昧な単複を英語が要求する場合がある。

Gendered pronouns

日本語sourceから性別を勝手に作らない。

Topic → syntax

「は」を単純subject化しない。

Honorific loss

英語へのprojection時にlossを記録する。

---

47. EN→JP固有処理

主要問題：

Pronoun suppression

英語のexplicit pronounを毎回訳さない。

Article deletion

"a/the"を直接表層化せずdiscourse featuresとして保存する。

Subject → topic decision

英語subjectを自動的に「は」へしない。

Politeness inference

英語原文には日本語ほど明示されないため、document styleから選択する。

Gender suppression

he/sheを毎回「彼/彼女」とする必要はない。

referentは内部で維持する。

---

48. Politeness Profile

翻訳開始時にtarget style profileを持てる。

例：

JP_TARGET_STYLE:
    politeness = polite
    register = neutral
    pronoun_explicitness = low
    literaryness = low

これがない場合はsource registerとdocument contextから推定する。

---

49. Idiom Translation

idiomはcomposition前にdetectする。

例：

«It's raining cats and dogs.»

を literal predicatesとして確定する前に、

IDIOM_HEAVY_RAIN

候補をsemantic forestへ追加する。

literal interpretationも必要なら残しておく。

Jevが文脈によって選択する。

---

50. Metaphor

idiom databaseだけでは足りない。

比喩は、

surface semantic reading
+
abstract intended reading

の複数hypothesisとして保持する。

target languageに同等のmetaphorが存在するなら維持する。

存在しなければsemantic translationへfallbackする。

---

51. Named Entities

固有名詞は翻訳ではなくidentity mappingを中心にする。

entity_id
canonical name
language-specific forms
reading
transliteration

を保持する。

例：

"東京"

"Tokyo"

は別predicateではなく同じentityのlanguage realizationである。

---

52. Terminology Memory

技術文書ではdocument単位またはproject単位で、

source term
concept
preferred target
forbidden target

を保持する。

これにより用語揺れを防ぐ。

---

53. Corpusの使い方

大量parallel corpusをend-to-end translation model学習だけに使わない。

JParaCrawl v3は約2,200万の英日sentence pairを含むため、大規模なconstruction correspondence抽出に使える。

用途：

lexical alignment

construction extraction

register statistics

collocation

translation preference

article inference statistics

pronoun omission statistics

semantic realization probability

など。

---

54. Construction Mining

parallel pair:

JP parse
↓
JLIR

EN parse
↓
JLIR

を比較する。

共通semantic subgraphに対して、

JP construction ↔ EN construction

を大量抽出する。

これにより人間が全翻訳規則を書く必要を減らす。

---

55. Statistical DataはRuleではなくPriorとして使う

例えば、

P(construction | semantic context)

を学習する。

しかし、

「corpusで頻度が高いから意味的に間違っていても採用」

はしない。

semantic constraintsが常に優先される。

---

56. Cache

Jev呼び出しコストを抑えるため、

decision cacheを持つ。

cache key例：

semantic_context_hash
candidate_set
document_style
decision_type

同一・類似decisionを再利用する。

---

57. Incremental Translation

document全体を最初から再翻訳しない。

Document Graphが更新された場合、

その変更に依存するdecisionだけinvalidateする。

例えば後続文で、

«山田さんは女性です。»

と分かった場合、

過去の曖昧なpronoun候補だけ再評価できる。

---

58. Delayed Commitment

この思想はシステム全体で徹底する。

parse ambiguity
word sense ambiguity
scope ambiguity
reference ambiguity
translation ambiguity

を可能な限りforestとして保持する。

必要になった時だけcollapseする。

---

59. Confidence

最終翻訳全体に単一confidenceを出すだけではなく、

feature単位にする。

例：

translation confidence: 0.94

reference:
    subject: 0.61

predicate:
    0.99

tense:
    1.00

register:
    0.84

これにより弱点が分かる。

---

60. Explainability

任意のtarget spanから逆引きできるようにする。

例：

"had already left"

← construction C921
← event E18
← completion feature A2
← source "もう帰った"
← decision JEV-1521

完全なtraceabilityを目標とする。

---

61. Failure Modes

システムは無理に常に1つの翻訳を返さない。

状態：

EXACT
GOOD
LOSSY
AMBIGUOUS
UNDERDETERMINED
UNSUPPORTED
UNPARSABLE

を持つ。

例えば性別が必要なのに不明なら、

"UNDERDETERMINED"

を返せる。

---

62. Interactive Disambiguation

本当に必要な場合のみユーザーへ質問できる。

例えば契約書で、

«彼»

のreferentが2人存在し、英訳で法的意味が変わるなら、

この「彼」は A と B のどちらを指しますか？

と聞く。

日常翻訳では自然な曖昧表現を使用する。

---

63. Round-trip Validation

JP→ENの場合：

JP
↓
JLIR_A
↓
EN
↓
JLIR_B

で比較する。

さらに、

EN
↓
JP
↓
JLIR_C

も検査可能。

ただし
