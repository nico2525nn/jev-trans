#!/usr/bin/env python3
"""Build the benchmark suites under corpus/bench/.

One corpus cannot answer whether the system got better. The Kenji corpus is
good at one thing -- prewar prose -- and optimising against it alone produces
the worst kind of progress report: classical-kana handling improves, the metric
improves, but nobody learns whether ordinary contemporary Japanese translation
improved at all.

So the bench is split into four suites with different jobs.

  synthetic    invariants: negation, tense, roles, zero pronoun, gender,
               scope, quantifiers, polite/plain, predicative adjectives.
               Small, each line close to a contract. A regression here is a
               broken promise, not a metric dip.
  modern       ordinary contemporary Japanese. Everyday register, deliberately
               common vocabulary, so the number measures the pipeline and not
               the dictionary.
  conversation short dialog sentences: omissions, pronouns, plain copula.
               Separate from modern because the hard part is different --
               zero anaphora and pronoun suppression, not vocabulary.
  literary     prewar prose. The Kenji corpus lives here so its numbers stop
               implying they describe the whole system.

Suite files hold {"text","dir"} records, dir being "ja-en" or "en-ja".
tools/stages.py --bench runs all of them and prints one table per suite.
"""

import json
import os

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "corpus", "bench")

# (text, direction)
SYNTHETIC = [
    # --- roles and case ---
    ("太郎が花子に本を渡した。", "ja-en"),
    ("太郎が本を読んだ。", "ja-en"),
    ("本を読んだ。", "ja-en"),
    ("私が昨日映画を見た。", "ja-en"),
    ("猫が鈴を鳴らした。", "ja-en"),
    ("空が青くなった。", "ja-en"),
    ("道が悪いので野原を歩く。", "ja-en"),
    ("本や紙を買った。", "ja-en"),
    # --- tense ---
    ("Taro read a book.", "en-ja"),
    ("I read a book.", "en-ja"),
    ("Taro gave a book to Hanako.", "en-ja"),
    ("She read a book.", "en-ja"),
    ("Taro spent money.", "en-ja"),
    ("The book is on the table.", "en-ja"),
    # --- pronouns and zero anaphora ---
    ("誰かが来た。", "ja-en"),
    ("誰が来た。", "ja-en"),
    ("誰かが云ふ。", "ja-en"),
    ("それを読んだ。", "ja-en"),
    # --- negation, scope, quantifiers ---
    ("皆が帰らなかった。", "ja-en"),
    ("誰も来なかった。", "ja-en"),
    ("本を買わなかった。", "ja-en"),
    # --- politeness and register ---
    ("太郎は本を読みました。", "ja-en"),
    ("太郎は本を読んだ。", "ja-en"),
    ("今日はいい天気ですね。", "ja-en"),
    # --- builtin-analyser staples ---
    ("花子が本を読んだ。", "ja-en"),
    ("太郎が来た。", "ja-en"),
    # --- predicative adjectives ---
    ("この本は面白い。", "ja-en"),
    ("空が青い。", "ja-en"),
]

MODERN = [
    "私は毎朝コーヒーを飲みます。",
    "この本はとても面白かったです。",
    "明日は雨が降るでしょう。",
    "彼は新しい仕事を始めました。",
    "子供たちは公園で元気に遊んでいます。",
    "会議は三時に始まりました。",
    "この問題はとても難しいです。",
    "私は毎日日本語を勉強しています。",
    "彼は最後にそう言いました。",
    "先生はとても親切です。",
    "私は友達と映画を見ました。",
    "この店の料理はおいしいです。",
    "私はご飯を作りました。",
    "彼は毎日走ります。",
    "私たちは一緒に暮らしています。",
    "明日は休みます。",
    "夏はとても暑いです。",
    "機械は高いです。",
    "その本はとても面白かったです。",
    "新しい店が開きました。",
]

CONVERSATION = [
    "さうだ。",
    "どうしたの。",
    "わかった。",
    "だめだよ。",
    "いいよ。",
    "もう帰った。",
    "知らない。",
    "食べてない。",
    "見たよ。",
    "そうかな。",
    "ちょっと待って。",
    "大丈夫。",
    "ありがとう。",
    "ごめん。",
    "そういえば。",
    "ほんと。",
    "行こう。",
    "食べよう。",
    "もういい。",
    "まだだよ。",
]


def main():
    os.makedirs(OUT, exist_ok=True)
    suites = {
        "synthetic": [{"text": t, "dir": d} for t, d in SYNTHETIC],
        "modern": [{"text": t, "dir": "ja-en"} for t in MODERN],
        "conversation": [{"text": t, "dir": "ja-en"} for t in CONVERSATION],
    }
    lite_src = os.path.join(
        os.path.dirname(os.path.abspath(__file__)), "..", "corpus", "sentences.json")
    lite = []
    if os.path.exists(lite_src):
        with open(lite_src, encoding="utf-8") as f:
            body = json.load(f)
        items = body if isinstance(body, list) else body.get("sentences", body)
        for s in items:
            t = s if isinstance(s, str) else s.get("text", "")
            if t and len(t) < 220:
                lite.append({"text": t, "dir": "ja-en"})
    suites["literary"] = lite
    for name, rows in suites.items():
        p = os.path.join(OUT, name + ".json")
        with open(p, "w", encoding="utf-8") as f:
            json.dump(rows, f, ensure_ascii=False, indent=1)
        print(f"wrote {p}: {len(rows)} sentences")


if __name__ == "__main__":
    main()
