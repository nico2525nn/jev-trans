#!/usr/bin/env python3
"""Measure JEV-Trans against real Japanese prose.

The acceptance target is one small Aozora Bunko book. This strips the ruby
markup from the archive text, splits it into sentences, translates each one,
and groups the failures by cause so the next fix is chosen by frequency rather
than by taste.

    python3 tools/corpus.py ingest corpus/akita.txt
    python3 tools/corpus.py measure corpus/akita.txt [--limit N] [--show N]
"""
import json
import re
import subprocess
import sys
import unicodedata

BIN = "./jevtrans"


# --- ingestion ------------------------------------------------------------

RUBY = re.compile(r"《[^》]*》")
KANA_ONLY = re.compile(r"[ぁ-ゖー]+$")


def strip_ruby(text):
    """Remove ruby readings and the | markers that delimit them."""
    out = RUBY.sub("", text)
    out = out.replace("｜", "")
    return out


def is_boilerplate(line):
    s = line.strip()
    if not s:
        return True
    if set(s) <= set("-＝=【】〔〕［＃#【rottle"):
        return True
    if s.startswith("【テキスト中に現れる記号について】"):
        return True
    if s.startswith("［＃"):
        return True
    if s.startswith("（例）") or s.startswith("："):
        return True
    return False


def is_rule(line):
    s = line.strip()
    return len(s) >= 3 and set(s) <= set("-=＝")


def body_lines(text):
    """Drop the Aozora header, the symbol key and the trailing note block.

    The archive layout is:

        title
        author
        <rule>
        【テキスト中に現れる記号について】 ... key ...
        <rule>
        body
        <rule>
        ［＃…］ notes

    So the body is between the SECOND and THIRD rule lines, and the earlier
    attempt took everything after the first, which kept only the key.
    """
    lines = text.splitlines()
    rules = [i for i, l in enumerate(lines) if is_rule(l)]
    if len(rules) < 3:
        start = rules[0] + 1 if rules else 0
        end = len(lines)
    else:
        start, end = rules[1] + 1, rules[2]
    out = [strip_ruby(l) for l in lines[start:end]]
    return "\n".join(l for l in out if not is_boilerplate(l))


SENT_END = re.compile(r"(?<=[。！？!?])(?![」』）])")


def sentences(text):
    """Split on Japanese sentence terminators, keeping paragraphs intact."""
    out = []
    for para in text.splitlines():
        para = para.strip()
        if not para:
            continue
        for s in SENT_END.split(para):
            s = s.strip()
            if s:
                out.append(s)
    return out


def ingest(path):
    raw = open(path, encoding="utf-8").read()
    body = body_lines(raw)
    sents = sentences(body)
    with open("corpus/plain.txt", "w", encoding="utf-8") as f:
        f.write(body)
    with open("corpus/sentences.json", "w", encoding="utf-8") as f:
        json.dump(sents, f, ensure_ascii=False, indent=1)
    print(f"{path}: {len(body)} chars of body, {len(sents)} sentences")


# --- measurement ----------------------------------------------------------

def translate(text, src="ja", tgt="en"):
    p = subprocess.run([BIN, "translate", "--text", text, "--src", src,
                        "--tgt", tgt, "--json"], capture_output=True, timeout=90)
    if not p.stdout.strip():
        return None
    d = json.loads(p.stdout.decode("utf-8"))
    sel = d["result"].get("selected")
    return {
        "text": (sel or {}).get("text"),
        "status": d["result"]["status"],
        "warnings": d.get("warnings") or [],
        "decisions": len(d["artifacts"].get("decisions") or []),
        "unknown": [
            u["surface"] for u in ((d["jlir"]["source"] or {}).get("sourceFeatures") or {}).get("unknowns", [])
        ],
        "predicates": [e["predicate"] for e in ((d["jlir"]["source"] or {}).get("events") or [])],
    }


def cause(r, sent):
    """The single most useful reason this sentence produced nothing."""
    if r is None:
        return "no output", []
    if r["text"]:
        return None, []
    if "UNKNOWN.VERB" in r["predicates"]:
        unk = [u for u in r["unknown"] if u]
        if unk:
            return "unresolved morpheme -> unknown predicate", unk
        return "predicate not in the lexicon", r["predicates"]
    if r["predicates"]:
        return "no construction for predicate", r["predicates"]
    return "no propositional content", []


def main():
    mode = sys.argv[1] if len(sys.argv) > 1 else "measure"
    if mode == "ingest":
        return ingest(sys.argv[2])
    limit = 40
    show = 6
    args = sys.argv[2:]
    if "--limit" in args:
        limit = int(args[args.index("--limit") + 1])
    if "--show" in args:
        show = int(args[args.index("--show") + 1])

    sents = json.load(open("corpus/sentences.json", encoding="utf-8"))[:limit]
    ok, groups, examples = 0, {}, {}
    for s in sents:
        r = translate(s)
        why, detail = cause(r, s)
        if why is None:
            ok += 1
            continue
        groups.setdefault(why, []).append(s)
        examples.setdefault(why, []).append((s, detail, r))

    total = len(sents)
    print(f"\n{ok}/{total} sentences translated ({100*ok//max(1,total)}%)\n")
    for why, ss in sorted(groups.items(), key=lambda kv: -len(kv[1])):
        print(f"  {len(ss):3}  {why}")
        for s, detail, _ in examples[why][:show]:
            print(f"         {s[:44]}")
            if detail:
                print(f"         -> {detail[:6]}")
        print()


if __name__ == "__main__":
    main()
