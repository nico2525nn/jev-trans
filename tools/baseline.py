#!/usr/bin/env python3
"""Capture or compare a behavioural baseline for the jevtrans CLI.

The refactor must not make any case worse. This records, per case, the selected
translation, the status and the loss vector, so a rewrite can be checked
against the behaviour it replaced instead of against hand-written expectations.

    python3 tools/baseline.py record > tools/baseline.json
    python3 tools/baseline.py compare tools/baseline.json
"""
import json
import subprocess
import sys

BIN = "./jevtrans"

JA_CASES = [
    "太郎が花子に本を渡した。",
    "私は行きます。",
    "彼女が来た。",
    "彼は本を読みました。",
    "彼は水を飲みました。",
    "太郎は本を花子に渡しました",
    "猫が犬を追った。",
    "学生が勉強しました。",
    "全員が帰らなかった。",
    "先生が来た。",
    "雨が降ったので開催した。",
    "私は行きます。嬉しそうでした。",
    "田中さんはビールを飲みました。",
    "本を読みました。",
]

EN_CASES = [
    "Taro gave a book to Hanako.",
    "She eats rice.",
    "I go.",
    "He drank water.",
    "They left.",
    "The teacher came.",
    "He read a book.",
    "A cat chased a dog.",
    "The book is on the table.",
]

EDGE_CASES = [
    ("", "ja", "en"),
    ("。", "ja", "en"),
    ("😀", "ja", "en"),
    ("ABC", "ja", "en"),
    ("The quick brown fox jumps over the lazy dog.", "en", "ja"),
    ("!!", "en", "ja"),
    ("非nings", "ja", "en"),
]


def run(text, src, tgt):
    args = [BIN, "translate", "--text", text, "--src", src, "--tgt", tgt, "--json"]
    try:
        p = subprocess.run(args, capture_output=True, timeout=60)
    except subprocess.TimeoutExpired:
        return {"text": None, "status": "TIMEOUT", "loss": None, "crash": False}
    if p.returncode != 0 or not p.stdout.strip():
        crash = b"panic" in p.stderr
        return {"text": None, "status": "CRASH" if crash else "ERROR",
                "loss": None, "crash": crash}
    d = json.loads(p.stdout.decode("utf-8"))
    sel = d["result"].get("selected")
    return {
        "text": (sel or {}).get("text"),
        "status": d["result"]["status"],
        "loss": (sel or {}).get("loss"),
        "crash": False,
    }


def cases():
    for t in JA_CASES:
        yield t, "ja", "en"
    for t in EN_CASES:
        yield t, "en", "ja"
    for t, s, g in EDGE_CASES:
        yield t, s, g


def record():
    out = {f"{s}->{g}::{t}": run(t, s, g) for t, s, g in cases()}
    json.dump(out, sys.stdout, ensure_ascii=False, indent=1, sort_keys=True)
    print()


def compare(path):
    base = json.load(open(path, encoding="utf-8"))
    now = {f"{s}->{g}::{t}": run(t, s, g) for t, s, g in cases()}
    worse, better, same = [], [], 0
    for k, b in base.items():
        n = now.get(k)
        if n is None:
            continue
        if n["crash"]:
            worse.append((k, b, n, "CRASH"))
        elif n["text"] is None and b["text"] is not None:
            worse.append((k, b, n, "lost translation"))
        elif b["text"] is None and n["text"] is not None:
            better.append((k, b, n))
        elif n["text"] == b["text"]:
            same += 1
        else:
            lb = sum((b["loss"] or {}).values())
            ln = sum((n["loss"] or {}).values())
            if ln > lb + 1e-9:
                worse.append((k, b, n, f"loss {lb:.3f} -> {ln:.3f}"))
            elif ln < lb - 1e-9:
                better.append((k, b, n))
            else:
                worse.append((k, b, n, "text changed, loss unchanged"))
    print(f"unchanged: {same}   better: {len(better)}   worse: {len(worse)}")
    for k, b, n, why in worse:
        print(f"  WORSE {why}: {k}")
        print(f"        was: {b['text']!r} [{b['status']}]")
        print(f"        now: {n['text']!r} [{n['status']}]")
    for k, b, n in better:
        print(f"  BETTER {k}: {b['text']!r} -> {n['text']!r}")
    return 1 if worse else 0


if __name__ == "__main__":
    mode = sys.argv[1] if len(sys.argv) > 1 else "record"
    if mode == "record":
        record()
    else:
        sys.exit(compare(sys.argv[2]))