#!/usr/bin/env python3
"""Stage-by-stage measurement of JEV-Trans on real prose.

plan2.md asks for more than a single translated/not-translated count, because
that number does not say which layer to fix next. This reports where sentences
survive and where they stop:

    morphology resolved     the analyser segmented the whole sentence
    predicate resolved      a clause head became an ontology predicate
    semantic frame resolved the event has the roles its frame calls for
    construction available  a target construction realizes it
    candidate generated     the realizer produced a surface string
    verification passed     the hard gate accepted it

    opaque span ratio       unresolved surfaces over all surfaces

A sentence is counted at the last stage it reached, so the stages sum to the
corpus total and the drop between two rows is exactly the work at that layer.

    python3 tools/stages.py corpus/sentences.json [--limit N]
"""
import json
import subprocess
import sys

BIN = "./jevtrans"

STAGES = [
    ("morphology resolved", "morphology"),
    ("predicate resolved", "predicate"),
    ("semantic frame resolved", "frame"),
    ("construction available", "construction"),
    ("candidate generated", "candidate"),
    ("verification passed", "verified"),
]


def measure(sentence):
    """Return the set of stages this sentence reached, plus opaque surfaces."""
    p = subprocess.run(
        [BIN, "translate", "--text", sentence, "--src", "ja", "--tgt", "en", "--json"],
        capture_output=True, timeout=90,
    )
    if not p.stdout.strip():
        return set(), []
    d = json.loads(p.stdout.decode("utf-8"))
    reached = set()

    morph = (d.get("artifacts") or {}).get("morph") or {}
    paths = morph.get("paths") or []
    if paths and paths[0].get("morphs"):
        reached.add("morphology")

    g = d.get("jlir", {}).get("source") or {}
    events = g.get("events") or []
    if events and not all(e["predicate"].startswith("UNKNOWN") for e in events):
        reached.add("predicate")
        # A frame is resolved when the event carries at least the roles the
        # ontology declares mandatory for its predicate. An empty argument map
        # means the frame was not filled, which is a different failure from not
        # knowing the predicate.
        if any(len(e.get("args") or {}) > 0 for e in events):
            reached.add("frame")

    # "construction available" means a construction was selected that actually
    # realizes the sense. The generic fallback frame is always selected for an
    # event that has one, so counting it would report 88/88 and say nothing; it
    # is excluded precisely because it carries no verb.
    art = d.get("artifacts") or {}
    proj = art.get("projection") or {}
    for e in proj.get("events") or []:
        c = e.get("construction") or ""
        if c and "FALLBACK" not in c:
            reached.add("construction")
            break

    cands = ((d.get("result") or {}).get("candidates")) or []
    if cands:
        reached.add("candidate")

    if (d.get("result") or {}).get("selected"):
        reached.add("verified")

    opaque = []
    mf = (art.get("morph") or {}).get("paths") or [{}]
    for m in (mf[0].get("morphs") or []):
        if not m.get("dict"):
            opaque.append(m.get("surface", ""))
    return reached, [o for o in opaque if o]


def main():
    path = sys.argv[1] if len(sys.argv) > 1 else "corpus/sentences.json"
    limit = None
    if "--limit" in sys.argv:
        limit = int(sys.argv[sys.argv.index("--limit") + 1])

    sentences = json.load(open(path, encoding="utf-8"))
    if limit:
        sentences = sentences[:limit]
    total = len(sentences)

    counts = {k: 0 for k, _ in STAGES}
    opaque_total = 0
    surface_total = 0
    last_stage = {k: 0 for k, _ in STAGES}
    unattributed = 0

    for s in sentences:
        reached, opaque = measure(s)
        surface_total += 1 + len(opaque)
        opaque_total += len(opaque)
        # Cumulative: reaching a stage implies reaching every earlier one, so
        # the rows are a funnel and the drop between two rows is the work at
        # that layer.
        hit = False
        for name, key in STAGES:
            if key in reached:
                counts[name] += 1
                hit = True
        if not hit:
            unattributed += 1

    print(f"\n{total} sentences\n")
    width = max(len(n) for n, _ in STAGES)
    prev = None
    for name, _ in STAGES:
        n = counts[name]
        delta = ""
        if prev is not None:
            lost = prev - n
            delta = f"   (-{lost})" if lost else ""
        print(f"  {name.ljust(width)}  {n:3}/{total}{delta}")
        prev = n
    if unattributed:
        print(f"  {'(stopped before morphology)'.ljust(width)}  {unattributed:3}/{total}")
    ratio = (opaque_total / surface_total * 100) if surface_total else 0.0
    print(f"\n  opaque span ratio      {opaque_total}/{surface_total} = {ratio:.1f}%")
    print()


if __name__ == "__main__":
    main()
