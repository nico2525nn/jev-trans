#!/usr/bin/env python3
"""Stage-by-stage measurement of JEV-Trans on real prose.

This script does not guess. It reads `stageMetrics`, which the pipeline emits
from the code that did the work, because the previous version re-derived every
stage from the response JSON and got four things wrong:

  - stages were counted independently, so a sentence could pass construction
    after failing the frame stage and the "funnel" went 26 then 44;
  - predicates counted as resolved if ANY clause head resolved;
  - a frame counted as filled if the argument map was merely non-empty;
  - candidates and verified were both read from the post-gate result, so they
    were the same number by construction.

The row labelled "verification passed" is now "equivalence proved" and reads
`certifiedCandidates`. Passing the hard gate and proving equivalence are
different claims: a candidate the verifier merely declined to reject is
eligible, not certified. The two are reported on separate lines below and must
not be read as one number.

Each of those made the next fix a guess. The pipeline now reports what it
actually did, cumulatively, and this script only sums it.

    $ python3 tools/stages.py corpus/sentences.json [--limit N] [--show N]

  morphology resolved     no opaque surfaces
  predicate resolved      every clause head became an ontology predicate
  semantic frame resolved every event carries its frame's mandatory roles
  construction available  a sense-realizing construction was selected, and the
                          generic fallback frame does not count
  candidate generated     the realizer produced something
  verification passed     the semantic hard gate accepted it
"""
import argparse
import json
import subprocess
import sys

BIN = "./jevtrans"

STAGES = [
    ("morphology resolved", "morphologyComplete"),
    ("predicate resolved", "predicatesComplete"),
    ("semantic frame resolved", "framesComplete"),
    ("construction available", "constructionsComplete"),
    ("candidate generated", "rawCandidates", "positive"),
    # `verified` is the legacy alias of `certifiedCandidates` and counts
    # candidates whose equivalence was PROVED. It is not "passed verification":
    # passing the semantic hard gate and proving equivalence are different
    # claims, and the label used to conflate them.
    ("equivalence proved", "certifiedCandidates", "positive"),
]

# The candidate counts, printed separately and never summed. raw is what the
# realizer produced; eligible is what survived the hard gate; certified is what
# the verifier actually proved; shortlisted is the list the output stage chose
# from; selected is what came out, which is at most one.
FUNNEL_KEYS = ("rawCandidates", "eligibleCandidates", "certifiedCandidates",
               "shortlistedCandidates", "selected")


def measure(sentence):
    """Return the pipeline's own metrics for one sentence."""
    p = subprocess.run(
        [BIN, "translate", "--text", sentence, "--src", "ja", "--tgt", "en", "--json"],
        capture_output=True, timeout=120,
    )
    if not p.stdout.strip():
        return None
    try:
        d = json.loads(p.stdout.decode("utf-8"))
    except json.JSONDecodeError:
        return None
    m = d.get("stageMetrics")
    if not m:
        # An older binary. Say so rather than silently reporting nothing.
        return {"__missing__": True}
    return m


def main():
    # argparse rather than index arithmetic into sys.argv: the docstring
    # documents --limit and --show, and a reader who types --help got a
    # FileNotFoundError naming the flag as a file. A tool whose documented
    # interface cannot be discovered is a tool whose documentation is ignored.
    ap = argparse.ArgumentParser(
        description="Stage-by-stage measurement of JEV-Trans on a sentence corpus.",
        epilog="The binary is rebuilt automatically; metrics are read from "
               "stageMetrics, never re-derived here.",
    )
    ap.add_argument("path", nargs="?", default="corpus/sentences.json",
                    help="JSON file holding a list of source sentences")
    ap.add_argument("--limit", type=int, default=None,
                    help="measure only the first N sentences")
    ap.add_argument("--show", type=int, default=5,
                    help="how many example sentences to print per stage (0 for none)")
    args = ap.parse_args()
    path, limit, show = args.path, args.limit, args.show

    sentences = json.load(open(path, encoding="utf-8"))
    if limit:
        sentences = sentences[:limit]
    total = len(sentences)

    counts = {name: 0 for name, *_ in STAGES}
    tokens = opaque = 0
    raw = eligible = certified = shortlisted = selected = 0
    reached_verb = 0
    missing_metrics = 0
    examples = {}
    losses = {}
    gaps = []

    for s in sentences:
        m = measure(s)
        if m is None:
            continue
        if m.get("__missing__"):
            missing_metrics += 1
            continue
        tokens += m.get("tokens", 0)
        opaque += m.get("opaqueTokens", 0)
        raw += m.get("rawCandidates", 0)
        eligible += m.get("eligibleCandidates", 0)
        certified += m.get("certifiedCandidates", 0)
        shortlisted += m.get("shortlistedCandidates", m.get("eligibleCandidates", 0))
        selected += m.get("selected", 0)
        for why in m.get("frameLosses") or []:
            losses[why] = losses.get(why, 0) + 1
        gaps.extend(m.get("predicateGaps") or [])

        for name, key, *kind in STAGES:
            if kind and kind[0] == "positive":
                ok = m.get(key, 0) > 0
            else:
                ok = bool(m.get(key))
            # Cumulative: a later stage only counts when every earlier one did.
            if not ok:
                break
            counts[name] += 1
            if name == "candidate generated":
                reached_verb += 1
                examples.setdefault(name, (s, m))

    print(f"\n{total} sentences\n")
    if missing_metrics:
        print(f"  ({missing_metrics} sentence(s) had no stageMetrics; rebuild the binary)\n")

    width = max(len(n) for n, *_ in STAGES)
    prev = None
    for name, *_ in STAGES:
        n = counts[name]
        delta = ""
        if prev is not None:
            lost = prev - n
            delta = f"   (-{lost})" if lost else ""
        print(f"  {name.ljust(width)}  {n:3}/{total}{delta}")
        prev = n

    ratio = (opaque / tokens * 100) if tokens else 0.0
    print(f"\n  opaque surfaces           {opaque}/{tokens} = {ratio:.1f}%")
    totals = dict(zip(FUNNEL_KEYS, (raw, eligible, certified, shortlisted, selected)))
    for label, value in (
        ("raw candidates", totals["rawCandidates"]),
        ("eligible (gate passed)", totals["eligibleCandidates"]),
        ("certified (proved)", totals["certifiedCandidates"]),
        ("shortlisted", totals["shortlistedCandidates"]),
        ("selected", totals["selected"]),
    ):
        print(f"  {label:<24} {value}")
    print(f"  dropped by the gate      {raw - eligible}")
    if losses:
        print("\n  why arguments were lost")
        for why, n in sorted(losses.items(), key=lambda kv: -kv[1]):
            print(f"    {n:3}  {why}")
    if gaps:
        print("\n  unresolved clause heads")
        # The three axes that decide who has to fix it. A gap that only appears
        # under one backend is that analyser's coverage; a gap whose head sits
        # on a fragment or a subordinate clause is the parser's; only the rest
        # is vocabulary nobody has written down yet.
        for key in ("tier", "backend", "pos"):
            counts = {}
            for g in gaps:
                k = g.get(key) or "(none)"
                counts[k] = counts.get(k, 0) + 1
            summary = ", ".join(f"{k}={v}" for k, v in sorted(counts.items(), key=lambda kv: -kv[1]))
            print(f"    by {key}: {summary}")
        by_cause = {}
        for gap in gaps:
            key = gap.get("cause", "?")
            by_cause.setdefault(key, []).append(gap)
        for cause, items in sorted(by_cause.items(), key=lambda kv: -len(kv[1])):
            heads = {}
            for g in items:
                heads[g.get("surface") or "?"] = heads.get(g.get("surface") or "?", 0) + 1
            distinct = len(heads)
            top = sorted(heads.items(), key=lambda kv: -kv[1])[:8]
            print(f"    {len(items):3}  {cause}  ({distinct} distinct heads)")
            for h, n in top:
                print(f"         {n:3}x {h}")
    for name, (s, m) in examples.items():
        print(f"\n  e.g. {name}: {s}")
        print("       " + json.dumps(m, ensure_ascii=False, sort_keys=True))
    print()


if __name__ == "__main__":
    main()
