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
    ("verification passed", "verified", "positive"),
]


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
    path = sys.argv[1] if len(sys.argv) > 1 else "corpus/sentences.json"
    limit = None
    show = 5
    args = sys.argv[2:]
    if "--limit" in args:
        limit = int(args[args.index("--limit") + 1])
    if "--show" in args:
        show = int(args[args.index("--show") + 1])

    sentences = json.load(open(path, encoding="utf-8"))
    if limit:
        sentences = sentences[:limit]
    total = len(sentences)

    counts = {name: 0 for name, *_ in STAGES}
    tokens = opaque = 0
    raw = eligible = certified = selected = 0
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
    print(f"  raw candidates           {raw}")
    print(f"  eligible (gate passed)   {eligible}")
    print(f"  certified (proved)       {certified}")
    print(f"  selected                 {selected}")
    print(f"  dropped by the gate      {raw - eligible}")
    if losses:
        print("\n  why arguments were lost")
        for why, n in sorted(losses.items(), key=lambda kv: -kv[1]):
            print(f"    {n:3}  {why}")
    if gaps:
        print("\n  unresolved clause heads")
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
