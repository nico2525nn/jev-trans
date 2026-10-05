# # JEV-Trans 次期アーキテクチャ改修指示

## 目的

現在のJEV-Transは、基本設計そのものよりも **knowledge coverage / proposal coverage** がボトルネックになっている。

現状の青空文庫「秋田街道」88文ベンチでは、翻訳成功は4/88程度で、主な失敗は以下。

```text
53  unresolved morpheme -> unknown predicate
30  no construction for predicate
 1  predicate not in lexicon
```

この結果から、単純に手書き辞書・sense・constructionを増やし続ける方針は採用しない。

また、

```text
source
→ Jevが文法解析を全部実施
→ JLIR
```

という完全Jev parser化も主経路にはしない。

Jevは引き続き **decision-only / arbitration model** として扱う。

---

# 1. 基本方針

現在の以下の思想は維持する。

- JLIR
- provenance
- ambiguity preservation
- uncertainty
- semantic verification
- target reparse
- hallucination rejection
- document state
- translation loss
- Jevによる有限候補選択
- 「分からないことを勝手に決めない」

変更するのは、

> 候補・語彙・構文をJEV-Trans内部の小さな手書きデータだけから供給する

という部分。

新しい基本構造は、

```text
external linguistic resources
          +
current rule engine
          ↓
multiple candidate analyses
          ↓
        Jev
    arbitration
          ↓
         JLIR
          ↓
generic target grammar
          +
large lexical resources
          ↓
candidate realization
          ↓
reparse + verification
```

とする。

---

# 2. Source Analysisをmulti-backend化する

現在の自作parserを削除しない。

ただし「唯一のparser」ではなくfallback/backendの1つに降格する。

目標構造：

```text
SOURCE
  │
  ├─ lexical/morphological backend
  │    ├─ Sudachi / SudachiDict
  │    └─ UniDic variants
  │
  ├─ deep grammar backend
  │    └─ Jacy / MRS
  │
  ├─ dependency backend
  │    └─ GiNZA / Universal Dependencies
  │
  └─ current internal rule parser
           │
           ▼
     Analysis Candidates
           │
           ▼
      Jev Arbitration
           │
           ▼
       JLIR lattice
```

重要：

Jev自身に自由形式で、

```text
この文章を解析してJSONを出せ
```

とは依頼しない。

各backendが候補を生成し、Jevは候補間の判定のみ行う。

---

# 3. 日本語形態素解析を自前辞書依存から外す

現在の主要failureである、

```text
unresolved morpheme
```

を大幅に減らす。

候補：

- Sudachi
- SudachiDict Full
- UniDic
- 旧仮名口語UniDic
- 近代文語UniDic

特に青空文庫では旧仮名・古い表記が頻出するため、入力文字列を大量の独自rewrite ruleで現代化するだけではなく、文体に適した辞書backendを選択可能にする。

理想：

```text
modern
qkana
kindai-bungo
```

などをbackend profileとして持つ。

現在のnormalizerは削除せず、trace付き補助処理として残す。

---

# 4. Lexiconを外部辞書から生成できるようにする

手書きの日本語→英語対応を主要sourceにしない。

少なくともJMdict等から、

```text
surface
lemma
POS
sense
English gloss candidates
```

を取得できるlexical provider interfaceを作る。

例：

```text
雲
 ↓
lexical provider
 ↓
cloud
clouds
cloud cover
...
```

この段階では訳語を確定しない。

Jevまたはsemantic constraintsがsense候補から選ぶ。

設計：

```go
type LexicalProvider interface {
    Lookup(source Lexeme, target Lang) []LexicalCandidate
}
```

複数providerを合成可能にする。

---

# 5. WordNet系sense resourceを導入可能にする

単なる対訳文字列だけではなく、

```text
surface
→ sense / synset
→ target lexicalization
```

という経路を使えるようにする。

Japanese WordNet等を候補sourceとして扱える設計にする。

重要なのは特定datasetへの密結合ではなく、

```go
type SenseProvider interface
```

を作ること。

---

# 6. Construction architectureを変更する

ここが最重要。

現在はpredicate/senseごとにtarget constructionが必要になりやすく、

```text
no construction for predicate
```

が大量発生している。

この方式はスケールしない。

今後は、

```text
predicate
→ valency/frame
→ generic construction
→ lexical realization
```

とする。

例えば、

```text
TRANSFER
Agent
Theme
Recipient
```

に対して、

```text
DITRANSITIVE
Subject = Agent
Object = Theme
IndirectObject = Recipient
```

というgeneric constructionを持つ。

`give`, `send`, `hand`, `lend`などの違いはconstructionではなくlexeme/frame mapping側で持つ。

---

# 7. Generic construction inventoryを作る

まず以下程度の一般構文へ集約する。

```text
INTRANSITIVE
TRANSITIVE
DITRANSITIVE
COPULAR

EXPERIENCER
COMMUNICATION
MENTAL_STATE
PERCEPTION
MOTION
TRANSFER

PASSIVE
CAUSATIVE
BENEFactive

MODAL
COMPLEMENT
RELATIVE_CLAUSE

COORDINATION
CONDITION
CAUSE
COMPARISON
QUANTIFICATION
```

constructionを数万個作るのではなく、

```text
数十〜数百 generic constructions
+
大規模lexicon
+
valency/frame metadata
```

でスケールさせる。

---

# 8. VerbNet系frame情報を利用可能にする

英語側ではVerbNet等の、

- thematic roles
- syntactic frames
- selection restrictions

を利用できるproviderを検討する。

目的は、

```text
SEND.XX
GIVE.XX
HAND.XX
```

それぞれに手書きconstructionを書くことではない。

それらを、

```text
transfer / ditransitive frame
```

にマッピングするために使う。

---

# 9. OpaqueNode / Partial Translationを導入する

現在最大の構造的欠陥は、

```text
未知語1つ
→ 文全体UNPARSABLE
```

となること。

これは変更する。

新しい最高位原則：

> unknown != failure

JLIRに未知部分を保持できるnodeを追加する。

例：

```go
type OpaqueNode struct {
    ID        ID
    Surface   string
    Lemma     string
    POS       string
    Span      Span
    TypeHints []string
    Reason    string
    Prov      []Provenance
}
```

例：

```text
私は見草を見た。
```

で「見草」が不明でも、

```text
SEE(
  experiencer = speaker,
  theme = Opaque("見草")
)
```

までは構築する。

---

# 10. PARTIAL statusを追加する

以下の状態を扱えるようにする。

```text
EXACT
GOOD
LOSSY
PARTIAL
AMBIGUOUS
UNDERDETERMINED
UNSUPPORTED
UNPARSABLE
DIVERGENT
```

`PARTIAL`は、

- 文構造は解析できた
- 一部lexemeのみ未解決
- 既知部分は安全に翻訳可能

な状態。

例：

```text
I saw [見草].
```

のような内部candidateを許容する。

UI/APIでは未翻訳spanを明示する。

---

# 11. OpaqueNodeを「翻訳文に紛れ込ませない」

以前の`unknowns.`問題を再発させない。

OpaqueNodeは普通のlexemeとしてrealizerへ流さない。

必ず、

```text
unresolved span
```

として専用扱いする。

PARTIAL出力の場合も、

- bracket
- source-preserving span
- metadata

のどれかで明確に区別する。

VerifierもOpaqueNode同士のidentityを追跡する。

---

# 12. Jevの役割を拡張するが、生成には使わない

新しいJevの主な役割：

```text
parser disagreement resolution
sense selection
dependency ambiguity
semantic role ambiguity
zero anaphora
scope
discourse relation
target lexical candidate selection
frame selection
construction selection
naturalness reranking
```

つまり、

```text
Jev = parser
```

ではなく、

```text
Jev = linguistic arbitration engine
```

とする。

---

# 13. Jev Tier-3 parser fallbackは許可する

外部parserでも十分な解析候補が作れない場合のみ、

Jevをstructured reconstructionに使用してよい。

ただし自由生成は禁止。

例えば、

```text
predicate candidates:
A
B
C
UNKNOWN

subject candidates:
e1
e2
UNKNOWN

object candidates:
e3
e4
UNKNOWN
```

のように、システム側が候補集合を作る。

Jevはそのgraph topologyを選択するだけ。

---

# 14. Source parser tier

将来的な優先順位：

```text
Tier 1
Jacy / precision grammar

Tier 2
UD / GiNZA / robust parser

Tier 3
current internal parser

Tier 4
Jev-assisted structured reconstruction

Tier 5
Opaque / partial analysis
```

ただし実装初期は依存導入の容易さに応じて順序を変えてよい。

---

# 15. Jacy / ERG / MRS統合

最終的には、

```text
Japanese
 ↓
Jacy
 ↓
MRS
 ↓
JLIR
```

および、

```text
English
 ↓
ERG
 ↓
MRS
 ↓
JLIR
```

をprecision backendとして導入する。

現在のJLIRは捨てない。

```text
MRS = deep semantic substrate
JLIR = translation state
```

とする。

JLIR側では、

- discourse
- zero anaphora
- pragmatics
- provenance
- uncertainty
- translation loss

を引き続き持つ。

---

# 16. Existing JaEn資産を調査する

DELPH-IN JaEnには、

```text
Jacy MRS
→ transfer
→ ERG MRS
```

という既存の日英transfer資産がある。

そのまま使用できなくても、

- transfer rules
- lexical mapping
- construction patterns
- failure handling

の知識sourceとして利用可能か調べる。

特に自動抽出されたtransfer rulesの設計を参考にする。

---

# 17. Corpusの役割を変更する

JParaCrawl等のparallel corpusを、

```text
翻訳モデルを学習する
```

ためではなく、

```text
lexical prior
construction prior
frame correspondence
register frequency
collocation
translation preference
```

を抽出するために使用する。

つまりcorpusはknowledge acquisition source。

---

# 18. 手書きlexiconを削除しない

現在の手書きknowledgeは、

```text
high-confidence curated layer
```

として残す。

provider priority例：

```text
project terminology
↓
curated JEV-Trans lexicon
↓
JMdict / WordNet
↓
corpus-derived candidates
↓
UNKNOWN
```

とする。

---

# 19. 手書きconstructionも削除しない

現在のconstructionは、

- irregular construction
- idiom
- highly language-specific realization

に限定して価値を残す。

genericな、

```text
X gives Y to Z
```

程度のものはframe grammarへ移行する。

---

# 20. Verification architectureは維持する

外部resourceを増やしても、

```text
source JLIR
↓
target generation
↓
target reparse
↓
target JLIR
↓
semantic verifier
```

は絶対に維持する。

むしろresourceが増えるほどVerifierが重要になる。

---

# 21. ParserとVerifierの独立性を上げる

可能なら、

source analysisとtarget reparseが全く同じheuristicしか使わない状態を減らす。

例えば、

```text
generation:
generic frame realizer

verification:
ERG/Jacy or independent parser
```

のように、異なる経路で検証できると自己一致バグを減らせる。

---

# 22. 直近の実装優先順位

次はこの順で進める。

## Phase A — degradation path

最優先。

- OpaqueNode
- PARTIAL status
- unresolved span tracking
- verifier support
- UI support
- corpus.pyでpartial coverageも測定

新指標：

```text
full translation coverage
partial translation coverage
semantic coverage
opaque span ratio
```

---

## Phase B — morphological/lexical coverage

- Sudachi/UniDic backend abstraction
- external Japanese dictionary provider
- JMdict lexical provider
- current internal lexiconとのmerge

この段階で、

```text
53 unresolved morpheme
```

を大幅に減らす。

---

## Phase C — generic constructions

- predicate-specific construction依存を減らす
- valency frame導入
- generic construction inventory
- sense → frame mapping
- frame → syntax mapping

これで、

```text
30 no construction
```

を減らす。

---

## Phase D — multi-parser

- GiNZA/UD
- Jacy/MRS
- parser candidate normalization
- Jev arbitration

---

## Phase E — external semantic knowledge

- Japanese WordNet
- VerbNet
- JaEn transfer resources
- corpus-derived priors

---

# 23. ベンチマーク方針

「秋田街道」88文は維持する。

ただし単一値4/88だけではなく、

```text
fully translated
partially translated
semantic structure recovered
predicate recovered
arguments recovered
lexical gaps
construction gaps
verification failures
```

まで計測する。

例：

```text
88 sentences

full:       21
partial:    51
failed:     16

predicate coverage: 91%
argument coverage:  84%
surface coverage:   76%
```

のようにする。

これでarchitecture改善と辞書追加を区別できる。

---

# 24. やらないこと

以下は禁止。

```text
× 失敗するたびに単語を1個手書き追加するだけ
× predicateごとにconstructionを作り続ける
× Jevに翻訳文を直接書かせる
× Jevに自由形式JSON解析をさせる
× unknownを通常lexemeとして生成する
× 未知語1つで全文を破棄する
× verifierを弱めてcoverageだけ増やす
```

---

# 25. 最終目標アーキテクチャ

```text
                    SOURCE
                       │
        ┌──────────────┼──────────────┐
        │              │              │
   Jacy/MRS        GiNZA/UD      Sudachi/UniDic
        │              │              │
        └──────┬───────┴───────┬──────┘
               │               │
       internal rule parser    │
               │               │
               └───────┬───────┘
                       ▼
               Analysis Forest
                       │
                Jev Arbitration
                       │
                       ▼
                  JLIR Lattice
                       │
              ┌────────┴────────┐
              │                 │
        known semantic      OpaqueNode
           structure         fallback
              │                 │
              └────────┬────────┘
                       ▼
                 Target Planner
                       │
             Semantic/Valency Frame
                       │
       ┌───────────────┼────────────────┐
       │               │                │
   Curated DB        JMdict          WordNet
       │               │                │
       └───────────────┼────────────────┘
                       │
                 lexical candidates
                       │
                     Jev
                       │
             Generic Construction
                       │
                 ERG/Jacy or
                 custom realizer
                       │
                  Candidates
                       │
                   Re-parser
                       │
                  Target JLIR
                       │
             Semantic Verification
                       │
                   Jev D9
                       │
                    OUTPUT
```

---

# 26. 設計原則

今後もこの原則を守る。

> Never guess what can remain unknown.  
> Never lose what is known.  
> Never generate what cannot be justified.

これに1つ追加する。

> **Unknown information must degrade locally, not destroy globally valid information.**

日本語：

> **未知の情報は局所的に劣化させ、既知の情報まで巻き添えにしない。**

これを新しいJEV-Transの第四原則とする。

---

# 最終判断

現在の4/88は、JEV-Transの中心思想が破綻した証拠ではない。

問題は、

```text
小規模手書きknowledge
×
predicate-specific realization
×
unknown = whole-sentence failure
```

という現在のcoverage戦略にある。

したがって全面的にJev parserへ戻すのではなく、

**既存の大規模言語資産をproposal generatorとして使い、Jevをその仲裁器として配置する。**

これを次期アーキテクチャの中心方針とする。