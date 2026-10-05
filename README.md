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
判断ログ、指示的状态を表示します。

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
  lex/                 形態素解析（日本語・英語）
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

---

## 現在の動作範囲

### 動作を確認したもの

- 18段の回路が双方向で実行され、各段が WebUI に描画されるトレースに記録される
- 単純な他動詞節・自動詞節が双方向で翻訳でき、往復も一致する

  | 入力 | 出力 | 状態 |
  |---|---|---|
  | 太郎が花子に本を渡した。 | Taro gave a book to Hanako. | GOOD |
  | 太郎は本を花子に渡しました | Taro gave a book to Hanako. | GOOD |
  | Taro gave a book to Hanako. | 太郎は本を花子に渡しましたね。 | LOSSY |
  | She eats rice. | 彼女は米を食べますね。 | LOSSY |
  | 私は行きます。 | I go. | LOSSY |
  | 彼女が来た。 | She came. | LOSSY |
  | 彼は水を飲みました。 | He drank a water. | LOSSY |
  | He drank water. | 彼は水を飲みましたね。 | LOSSY |

- 原文が供給しない性別が生成されることはありません
- スコープの曖昧性は解決されず、`皆が帰らなかった` は2読みを保持します
- 検証器は再構文解析で命題が変化した候補を却下し、却下理由を記録します
- 空文字列・絵文字・句読点のみ・非日本語文字混在の入力で panic しません

### 未対応・制約

以下は実際の不足です。

- **語彙カバレッジ**。日本語辞書は約750表層で、英語解析器の一般名詞・動詞の
  辞書は意図的に小さくしています。未知の動詞・名詞は推測せず `UNKNOWN`
  となり、カバレッジ警告とともに記録されます。構築選択は系統単位の
  キーで行われるため、`追う`（chase）が系統の "go" として実現され
  `LOSSY` になることがあります。
- **量化詞**。`全員が帰らなかった` は英語の `ALL` を失います。量名詞を
  実体化する構築が未登録のためです。検証器はこれを検知し、"did not go"
  を出さないよう候補を却下します。
- **二節文**。ので/ば/けど などで連結された文は節への解析までは行われます
  が、第二節の生成が未実装で `UNPARSABLE` になります。
- **英語再構文解析のカバレッジ**。英語解析器の一般名詞・動詞の辞書が
  小さいため、未知の語を含む文の再解析は `UNKNOWN.VERB` となり
  検証器が unparsable として却下します。
- **判断モデルの実API未検証**。クライアントは `systemone` の仕様どおりに
  実装され、prior へ劣化する経路は動作しますが、この環境には
  `OPENCODE_API_KEY` がないため実APIへのリクエストは行っていません。

---

## ライセンス

[Apache License 2.0](LICENSE) — Copyright 2026 nico2525nn
（詳細は [NOTICE](NOTICE)）

判断モデルとして利用する Jev / OpenCode Zen は実行時に外部サービスへ
接続しますが、同梱していません。`OPENCODE_API_KEY` が未設定の場合は
ネットワーク要求を一切行いません。