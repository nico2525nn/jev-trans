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

現在（`tools/baselines/session-diff.md` に全表）:

```
                              4eae9af   current
morphology fully resolved            68        68
predicate  fully resolved             25   →    31
semantic frame fully resolved          9   →    28
unresolved clause heads               70   →    61
raw candidates                       162   →   216
eligible (gate passed)                 1   →     4
certified (equivalence proved)         0        0
selected                               1   →     4
```

引数が失われた理由（`tools/baselines/session-diff.md`）:

```
no_arguments_bound                   49   →     0
unknown_predicate:UNKNOWN.VERB        70   →    61
missing_role:recipient                 1   →     4
missing_role:comitative                3   →     4
missing_role:theme                     2   →     3
missing_role:experiencer               2   →     0
```

**`no_arguments_bound` 49 → 0 が実質的な変化です。** parser は日本語の省略主語を
明示的な零 phrase として計画しますが、analyzer は「不在」しか見ていませんでした。
両層的主語の形について認識が食い違い、その結果、日本が主語を落とした文は
すべて引数のない event で終わっていました。引数 binder 自体には何も問題が
ありませんでした — 渡ってきた節がすでに壊れていたのです。「引数が結べなかった」
という報告が指していたのは binder ではなく、その上流の矛盾でした。+19 の
frame 解決もここから出ています。

**`certified` は 0 のままです。** hard gate を通ることと等価性を証明することは
別の主張で、このコーパスではまだ証明された候補が一つもありません。4 つの選択
済み候補はいずれも LOSSY です。Verifier を緩めて数字を上げることは、ここでは
やってはいけません。

残りの内訳は `tools/stages.py` の `unresolved clause heads` に出ます。
61 件中、50 件は語彙知識（`落ち` `ゆらい` `きらめき` — 宮沢賢治 1920 年代の
語彙）、6 件は助動詞が句頭になったもの、5 件は述語のない断片です。
LexicalProvider を入れたのはこの分類がそのまま手を打つ先を示すからです。
`JEV_LEXICON_TSV` に外部辞書を与えることで、core を変えずにカバレッジを
広げられます。この環境には外部辞書が無いので、まだその数は動いていません。

分類を出すのは `tools/stages.py` で、種別・parser の添付階層・backend・品詞の
4 軸です。この 4 軸が無ければ 61 件は「全部辞書不足」としか読めず、辞書と
構文解析と形態素解析のどこを直すべきかは分かりません。実際に、この分類を
作ったことで 代名詞の POS 欠落、copula 判定、主語 zéro句の不整合、そして
〜くなる 構文の 4 件が見つかりました。

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