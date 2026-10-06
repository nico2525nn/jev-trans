# JEV-Trans

日本語と英語の双方向制約ベース翻訳システムです。

このシステムは翻訳文を生成しません。解析し、曖昧な箇所を保持し、生成に
影響する曖昧さだけを判断モデルに問い、制約のもとで候補を組み立て、各候補を
再度構文解析して意味的に照合してから出力します。

仕様に基づく設計の詳細は [`plan.md`](plan.md) にあります。パッケージ間の
インターフェースは [`docs/contracts.md`](docs/contracts.md) にあります。

---

## 必要なもの

- Go 1.22 以上（開発は 1.26）
- 外部依存なし（標準ライブラリのみ）

## 動かす

```sh
go build ./cmd/jevtrans
./jevtrans serve --addr 127.0.0.1:8080
```

ブラウザで `http://127.0.0.1:8080` を開くと WebUI が起動します。

```sh
./jevtrans translate --text "太郎が花子に本を渡した。"
./jevtrans translate --text "Taro gave a book to Hanako." --src en --tgt ja
./jevtrans translate --text "私は行きます。" --json     # 回路を含む全出力を取得
./jevtrans ontology --search transfer                   # 述語オントロジを閲覧
./jevtrans lex --query 渡す                              # 語彙を検索
```

API キーを設定しない場合も全機能が動作します。この場合、判断は決定論的な
解析priorで解決され、その旨はトレース上のすべての判断に
`source=prior` として記録されます。

---

## パイプライン

`plan.md` §6 の図に対応する18段構成です。各段は `internal/trace` に記録され、
WebUI がその記録を描画します。

```
DOCUMENT STATE
   │  INPUT NORMALIZATION
   ▼
MORPHOLOGICAL LATTICE ──► PACKED SYNTACTIC FOREST ──► SOURCE SEMANTIC FOREST
                                                              │
                                                              ▼
                                            JLIR CORE（9層）
                                                              │
                                            JEV DECISION GRAPH（D0..D9）
                                                              │
                                            CONSTRAINED JLIR STATE
                                                              │
                                            TARGET PROJECTION ENGINE
                                                              │
                                            TARGET MESSAGE PLANNER
                                                              │
                                            CONSTRUCTION SELECTION
                                                              │
                                            PACKED REALIZATION FOREST
                                                              │
                                            GRAMMATICAL REALIZER
                                                              │
                                            candidate texts
                                                              │
                                            TARGET RE-PARSER
                                                              │
                                            TARGET JLIR
                                                              │
                                            SEMANTIC EQUIVALENCE VERIFIER
                                                              │
                                            JEV RERANKER
                                                              ▼
                                                        FINAL OUTPUT
```

---

## 実装上の性質

### 保存と実現を分離する

`entity = speaker` などの意味的指示は、日本語では表層が文字列ゼロでも
保たれます。意味の保存と表層の保存は別の性質として扱います。

### 情報を追加しない

意味的な特徴には必ず由来（provenance）が付きます。原文が支持しない内容を
対象言語が主張した場合、その候補は `UNSUPPORTED` として却下されます。

```
「先生が来た。」 → "The teacher arrived."   → 受理、pragmatic 損失として記録
「先生が来た。」 → "The female teacher came." → 由来のない gender=female により却下
```

### 曖昧性を保持する

`皆が帰らなかった` は解析時点で一つに決められません。`NOT > ALL` と
`ALL > NOT` の2読みを持つスコープノードのまま保留されます。零代名詞も
第一級の対象として扱われ、参照先は分布として保持されます。

```json
{"referent": {"probabilities": {"e1": 0.56, "e2": 0.43, "UNKNOWN": 0.01}}}
```

### 損失を測定する

```
propositional  referential  temporal  pragmatic  stylistic  implicature
     0.00          0.00        0.00       0.10       0.00        0.00
```

日本語の尊敬表現が英語に存在しない場合、それは `pragmatic` 損失として
記録されます。提案内容の変化ではありません。

### 検証器がハードゲートである

生成された候補は必ず再度構文解析され、特徴ごとに照合されます。自然さの
評価が意味的に誤った文を救うことはありません。

### 「わからない」と言える

状態は `EXACT` / `GOOD` / `LOSSY` / `AMBIGUOUS` / `UNDERDETERMINED` /
`UNSUPPORTED` / `UNPARSABLE` を取ります。英語が要求する区別を原文が
供給できない場合、推測せずに `UNDERDETERMINED` を返します。

---

## 判断モデル

システム内で使用するモデルは [Jev](https://docs.typesafe.ai/) のみで、
OpenCode Zen 経由で `systemone` エンドポイントを呼びます。

```
POST https://opencode.ai/zen/v1/systemone
model: jev-1.13-free
```

型付き質問（`choice` / `noul` / `score`）のみを送り、値と確率、
confidence を受け取ります。自由記述の生成は行いません。候補集合は常に
文法・辞書・オントロジ・構文森からシステムが生成します。

判断は固定のDAGです。

| | | |
|---|---|---|
| D0 語彙分割 | D1 構文の曖昧性 | D2 述語 senses |
| D3 意味的役割 | D4 照応・零代名詞 | D5 discourse |
| D6 語用解釈 | D7 target projection | D8 construction選択 |
| D9 最終ランキング | | |

翻訳結果に影響しない判断は問いません。候補が2つあっても target 上の
実現が同一なら質問は省略され、その理由がトレースに記録されます。
回答は `hash(意味的文脈, 候補集合, スタイル, 判断種別)` でキャッシュします。

---

## WebUI

テキスト入力と出力だけではありません。パイプライン図、各段のスパン、
形態素格子、節構造、JLIR グラフ、生成森と却下された枝とその理由、
判断ログ、指示的状態を表示します。

- 回路図は段落順に点灯し、各ノードに所要時間と状態を表示します
- JLIR グラフは entity / event / 役割辺 / scope 帯を描画し、
  未解決の `referent` は上位3候補の確率を表示します
- entity をクリックすると、その特徴と provenance（origin, token, confidence,
  decision id）が表示されます
- 判断ログは `jev` / `cache` / `prior` / `skipped` を区別して表示し、
  省略された判断は理由を示します

API キーが無い環境では `ORACLE OFFLINE — analysis priors only` と表示します。

---

## 構成

```
cmd/jevtrans/          CLI: serve / translate / trace / ontology / lex
internal/
  lang/                言語・スクリプト識別
  trace/               各段が書き込む観測スパン
  jlir/                9層のIR（意味・指示・時間・discourse・情報構造・
                       語用・言語固有形・不確実性・由来）
  forest/              森：形態素・構文・生成
  syntax/              Clause / Phrase / Bundle（構文解析と意味層の接合点）
  lex/                 形態素解析（日本語・英語）と MorphAnalyzer backend 枠。
                      builtin / process（Sudachi、UniDic、MeCab はこの枠で追加）
  lexgen/              標的言語への語彙解決（名詞・固有名詞・代名詞）
  ontology/            述語在庫（役割枠と階層、219 senses / 21 families）
  lexicon/             表層→sense、慣用句、用語メモリ
  semantics/           構文→JLIR、source semantic forest、scope、語用
  discourse/           文書状態、指示同一性、零代名詞、用語記憶
  jev/                 System One クライアント、キャッシュ、判断DAG
  plan/                projection、構築ライブラリ、制約ソルバ、
                       英語・日本語リアライザ
  verify/              target 再構文解析、等価検証、損失ベクタ、
                       幻覚検出、Pareto ランキング
  pipeline/            オーケストレーション
  server/              HTTP API と埋め込み WebUI
```

---

## 設定

| 変数 | 意味 |
|---|---|
| `OPENCODE_API_KEY` | 判断モデルを有効化する。未設定なら prior で動作し、トレースに記録される |
| `JEV_ADDR` | `serve` の既定待ち受けアドレス |
| `JEV_SUDACHI_ADAPTER` | Sudachi アダプタのパス。自動探索が効かない場合の明示指定 |
| `JEV_SUDACHI_DICT` / `SUDACHIDICT_DIR` | 使う SudachiDict ビルド（small / core / full、旧仮名は国語研 UniDic） |
| `JEV_LEXICON_TSV` | 外部の述語辞書（TSV）。述語知識の供給元を拡張する。ファイルが無い場合は使われずに忽略される |

### 述語知識の供給元（LexicalProvider）

述語知識は `internal/lexicon.Provider` の向こう側にあり、core は供給先を知りません。
バイナリに組み込まれているのは curation 済みのテーブルだけで、外部の辞書は
同じ役割を演じます。core を書き換えずにカバレッジを広げられます。

```tsv
# 出典をここに書く。トレースがこの行を読む
落ちる	MOVE.05:0.90
きらめき	MOVE.15:0.60,MOVE.01:0.30
```

- **チェーンは先勝ち**です。curation 済みテーブルが知っているものを外部辞書が
  上書きすることはありません。一般辞書の第1義は文脈が求める読みとは限らず、
  それを許すと、動作している部分が黙って劣化します。
- **オントロジは閉集合**です。sense をここに書いても、オントロジに無い id は
  捨てられます。provider は語彙を*広げる* もので、sense を*増やす* ものではありません。
  sense は語についてではなく世界についての一主張なので、ontology 自身の変更として
  review されるべきです。
- **ファイルが無い = 未設定**、異常ではありません。トレースにはconsult した provider 名が残ります。

---

## 現在の動作範囲

### 達成目標と、そこからの距離

このシステムの目標は、**青空文庫の小振りな作品1冊を通しで翻訳すること**です。
手作りの例ではなく実際の文章で測らないと、実用になるかどうかを
判断できないためです。

現在計測しているコーパスは宮沢賢治「秋田街道」1,898字・88文です。

```
$ python3 tools/corpus.py ingest corpus/akita.txt
$ python3 tools/corpus.py measure corpus/akita.txt --limit 88

4/88 sentences translated (4%)

   53  unresolved morpheme -> unknown predicate
   30  no construction for predicate
    1  predicate not in the lexicon
```

**つまり目標はまだ達成していません。** 88文のうち翻訳できたのは4文、率にして4%です。
この数字を見やすく整えて書くことはしません。

### 形態素解析バックエンド

形態素解析は**交換可能な backend** に切り出してあります。core は標準ライブラリ
のみで巨大な辞書を一切持ちません。

```go
type MorphAnalyzer interface {
    Name() string
    Version() string
    Supports(p Profile) bool
    Analyze(ctx context.Context, text string, p Profile) (*Analysis, error)
}
```

```
jev-trans core
      │  NDJSON（1行1要求・1行1応答）
      ▼
┌──────────────────┐
│ morph backend    │
├──────────────────┤
│ builtin          │  自前の約800語。依存ゼロのフォールバック
│ Sudachi          │  process/sudachi_backend.py → sudachipy
│ UniDic / MeCab   │  process として同じ枠で追加できる
└──────────────────┘
```

**process 境界を選んだ理由** — Sudachi は Python 拡張、UniDic は通常 MeCab 経由で、
辞書はどれも数百MB。Go の import に置けば core は依存フリーでも小さくもなくなり、
さらにネイティブツールチェーンがビルドに必要になります。pipe の上であれば core は
`os/exec` と JSON だけで足ります。**外部解析器の無い環境でも停止しません。**

設定別の lexicon：

| profile | 担当 |
|---|---|
| `modern` / `modern-literary` | SudachiDict |
| `old-kana-colloquial` | 国語研 旧仮名口語UniDic |
| `auto` | テキストから判定して比較 |

バージョンは探索せず **pin して記録**します。ただし実際の SudachiPy の stable は
**0.7.0** で、V1 SudachiDict を読みます（`0.8.2` は Java 版 Sudachi のリリースです）。
どの backend が答えたかは span の `analyzer` ラベルに入ので、
ビルトインに落ちた run とそうでない run は区別できます。

```sh
./jevtrans translate --morph auto --sudachi-dict core --morph-profile auto --text "..."
./jevtrans translate --morph builtin --text "..."      # 外部を使わず強制
```

### 計測方法

```sh
python3 tools/corpus.py ingest corpus/akita.txt   # ルビ除去・文分割
python3 tools/corpus.py measure corpus/akita.txt  # 文ごとに翻訳して失敗原因で集計
python3 tools/baseline.py record > /tmp/b.json    # 回帰用スナップショット
python3 tools/baseline.py compare /tmp/b.json
```

`measure` は失敗を原因ごとに束ねます。次の修正を「頻度で選ぶ」ためで、
勘で直さないためです。段階別にどこで落ちているかを見るには `stages.py` を使います。

```
$ python3 tools/stages.py corpus/sentences.json

  morphology resolved       88/88
  predicate resolved        51/88   (-37)
  semantic frame resolved   26/88   (-25)
  construction available    49/88   (-23)
  candidate generated        2/88   (-47)
  verification passed        2/88

  opaque span ratio      162/250 = 64.8%
```

行は累積です。ある段まで到達していれば、それ以前の段にも到達しています。だから
2行の差がそのままその層の仕事量になります。

### 実際に翻訳できるもの

英語→日本語方向は動作しています。**日本語→英語方向は Sudachi 化で壊れています。**
以下の表は現行バイナリで検証した結果のみです。

| 入力 | 出力 | 状態 |
|---|---|---|
| Taro gave a book to Hanako. | 太郎は本を花子に渡しましたね。 | LOSSY |
| She eats rice. | 彼女は米を食べますね。 | LOSSY |
| He drank water. | 彼は水を飲みましたね。 | LOSSY |
| 太郎が花子に本を渡した。 | （候補なし） | UNPARSABLE |
| 私は行きます。 | （候補なし） | UNPARSABLE |
| 彼女が来た。 | （候補なし） | UNPARSABLE |

日本語→英語が壊れた理由は、regression の節で述べたとおりです。`--morph builtin` を
付ければ builtin 解析器では従来どおり英語方向の例は動きます。

**偽の保証は出ていません。** 解析できない述語は語彙化されず、候補は生成されず、
`UNPARSABLE` として報告されます。プレースホルダー語（`unknowns.` など）は出力に
一切現れません（`TestNoPlaceholderEverReachesTheOutput` で固定）。これは
plan.md §22 の要求です。

その他の固定されている性質:

- 原文が支持しない性別が生成されることはない
- スコープの曖昧性は解決されず、`皆が帰らなかった` は2読みを保持する
- 検証器のハードゲートは候補数に関係なく必ず実行される
- 形態素カバレッジは常に100%（未知語も1トークンとして保持）
- `ー` を含む外来語（ビール、コーヒー、テーブル）が1形態素になる
- 空白のみ・絵文字・句読点のみ・非言語混在で panic しない

### コーパス測定で判明し、直したもの

実文を測るまで分からなかった問題がありました。多くは **アーキテクチャではなく
知識の欠落** でした。ただし Sudachi については逆で、adapter が呼んでいた API が
`Dictionary() → .tokenizer()` ではなく `sudachipy.Tokenizer(...)` だったため起動の
たびに落ちていました。そのエラーを辞書形式の非互換と誤診し `0.8.2` を pin して
いましたが、`0.8.2` は Java 版 Sudachi のリリースであり SudachiPy には存在しません。
実際の stable は **0.7.0** で、V1 辞書を読みます。

| 問題 | 実測された症状 | 対処 |
|---|---|---|
| 旧仮名遣い | 宮沢は `ゐる` と書く。現行辞書には `いる` しか無い | 外部解析器が無い場合のフォールバックで `ゐ→い` `ゑ→え` `ふ→う` と反復記号を展開。書き換えはトレースに記録 |
| 辞書の二重性 | 述語辞書241語と形態素辞書836語が別物。片方にしか無い動詞は分割不能で `住っていた` が `住っ/て/いた` に崩れた | 述語辞書が `lex.RegisterJapaneseVerb` を通じて解析器に動詞を供給。活用級は綴りから推定 |
| い形容詞述語 | `この道は古い。` には動詞が無い。文が丸ごと死に、診断が空文字になった | COPULA 系で解決。register で sense を選ぶ |
| 過剰に厳しい役割検査 | 時制・様式の付随語をフレームが持てないだけで文を却下 | 核心項の脱落は致命的のまま。付随語は許容 |
| polite 形と copula | です/ます/ました/ましてが辞書に無く、COPULA.03/.05 に英語構築も無かった | 両方追加 |
| Sudachi adapter の API | 起動ごとに `cannot create Tokenizer instances`、辞書を辞書非互換と誤診 | 正しいコンストラクタに修正。backend が実際に応答 |
| adapter パス | 作業ディレクトリ基準で、`go test` では package 配下 → python が即死し、原因不明の stream 閉鎖 | 実行ファイルと作業ディレクトリの親を探索。stderr をエラーに添付 |
| `translate` の backend 未登録 | `serve` のみ had it、CLI の計測は全て builtin を測っていた | セットアップを共有 |
| backend の失敗理由 | trace には残るが返却値には無く、「設定されて失敗」と「未インストール」が同一に見えた | 理由を返却値にも載せる |
| 文字 vs バイト offset | Sudachi は文字、core はバイト → 日本語の span がすべてズレ | adapter で変換 |

Sudachi が応答するようになった後の効果：

```
                     builtin   Sudachi
opaque span ratio     80.6%  →   64.8%
predicate resolved     43    →    51
construction avail.    39    →    49
```

**担当した層は予想どおり動きました。**

### ただし regression がある

```
candidate generated      5    →     2
```

実在の regression です。Sudachi の細かい分割が copula 補助動詞・単独のい形容詞・
複数トークン引数を下流に晒し、projection がまだ扱えていません。copula イベントは
theme 付きで解決しますが subject が空のため、realizer が何も出しません。

これは「述語知識が 37 件残っている」に加えて、**解析は良くなったが下流が追いついて
いない**という別の層が露出したことを意味します。段階別指標がそれを正確に指すので、
次はそこを直します。推測ではなく計測で次の場所が分かります。

### まだ足りていないもの

段階別指標の順に、次の層が順に減っています。

1. **candidate generated の regression**（Sudachi 化で 5 → 2）。下流が Sudachi の
   細かい分割に追いついていません。ここを先に直します。
2. **述語知識が 51 件**。Sudachi が分割できた動詞でも、述語辞書にはまだ
   無い動詞があります。ただし内訳は 1 つの原因ではありません。
   `tools/stages.py` の `unresolved clause heads` が区別します。

   ```
   51  unknown_lexeme  (35 distinct heads)   語彙の問題
    6  auxiliary       (2 distinct heads)    構文解析の問題
    5  no_clause_head                         分割の結果
   ```

   35 heads の多くは宮沢賢治 1920 年代の語彙 — `落ち` `きらめき` `置きすて`
   `浮ん` — で，手で追記してもこの作品のためだけになります。
   そこで LexicalProvider を入れました。Supply 元は差し替え可能で、
   `JEV_LEXICON_TSV` に TSV を渡すだけで core は触らずにカバレッジが広がります。
   この環境には外部辞書が無いので、头上的数値はまだ変わっていません。
3. **意味フレームが 25 件**。述語は分かったが、役割が埋まらない文。
4. **英語構築が 13 件**。sense は取れても対象言語の構築が無い。
5. **名詞の語彙**。`見草` `雲` `沢` など、この作品の地名が未収録で、
   それらが主語・目的語になる文は生成できません。
6. **命令・義務の枠**。`Taro should read a book.` は扱っていません。
7. **量化詞**。`全員が帰らなかった` の `ALL` は失われます。
8. **二節文**。ので/ば/けど などで連結された文は `UNPARSABLE` です。
9. **判断モデルの実API未検証**。クライアントは `systemone` の仕様どおりに
   実装され、prior へ劣化する経路は動作しますが、この環境には
   `OPENCODE_API_KEY` がないため実APIへのリクエストは行っていません。

### 構造的な欠陥：劣化経路が無い

これは機能不足ではなく、設計の問題です。

未知の語が1つあると、その文は丸ごとゼロになります。「捏造しない」と
「何も出さない」が同じコードパスだからです。88文のうち84文が、文中の
1語を処理できないという理由だけで落ちています。

plan.md が要求する「臆測しない」を守る正しい方法は、解析できた部分は訳し、
訳せなかった箇所を明示的にマークして返すことです。現在はその中間状態が存在しません。
これを追加するのが、到達率を一番動かす変更になります。語彙を増やすのとは
別の、そして構造的な作業です。

### テスト

`go test ./...` で7パッケージ・約2,000行が走ります。実装ではなく
plan.md の約束を固定しています。

| テスト | 固定する約束 |
|---|---|
| `TestNoPlaceholderEverReachesTheOutput` | プレースホルダー語が出ないこと |
| `TestUnresolvedPredicateIsReportedNotGuessed` | 未解決述語は `UNPARSABLE` |
| `TestEveryCircuitStageRuns` | §6 の18段が全て走ること |
| `TestNoGenderIsInvented` | §4 / §22 |
| `TestMorphemeCoverageIsTotal` | 文字が脱落しないこと |
| `TestKatakanaLoanwordIsOneToken` | `ー` による外来語の分裂 |
| `TestScopeAmbiguitySurvivesAnalysis` | §13 |
| `TestScopeRepresentationsAgree` | スコープ重みの二重表現の不一致 |
| `TestVerifyIsIdempotentOnIdenticalGraphs` | 検証器の較正 |
| `TestRealizeIsIndependentlyExercisable` | §36 を単独実行できること |
| `TestNoPanicOnHostileInput` | 堅牢性 |

---

## ライセンス

[Apache License 2.0](LICENSE) — Copyright 2026 nico2525nn
（詳細は [NOTICE](NOTICE)）

判断モデルとして利用する Jev / OpenCode Zen は実行時に外部サービスへ
接続しますが、同梱していません。`OPENCODE_API_KEY` が未設定の場合は
ネットワーク要求を一切行いません。