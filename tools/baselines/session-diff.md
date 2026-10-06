Corpus: 76 sentences from corpus/akita.txt (Miyazawa Kenji, 1920s).
Compared 4eae9af (session start) with the current tree. Counting is per
sentence: a stage counts only when every earlier stage counted for that
sentence, so these are cumulative funnel numbers and not independent.

| metric | 4eae9af | current | delta |
|---|---|---|---|
| morphology fully resolved | 68 | 68 | +0 |
| predicate fully resolved | 25 | 31 | +6 |
| semantic frame fully resolved | 9 | 28 | +19 |
| unresolved clause heads | 70 | 61 | -9 |
| raw candidates | 162 | 216 | +54 |
| eligible (gate passed) | 1 | 4 | +3 |
| certified (equivalence proved) | 0 | 0 | +0 |
| selected | 1 | 4 | +3 |

Argument losses:

| cause | 4eae9af | current | delta |
|---|---|---|---|
| `missing_role:agent` | 1 | 0 | -1 |
| `missing_role:comitative` | 3 | 4 | +1 |
| `missing_role:experiencer` | 2 | 0 | -2 |
| `missing_role:recipient` | 1 | 4 | +3 |
| `missing_role:theme` | 2 | 3 | +1 |
| `no_arguments_bound` | 49 | 0 | -49 |
| `unknown_predicate:MOVE.01` | 1 | 1 | +0 |
| `unknown_predicate:UNKNOWN.VERB` | 70 | 61 | -9 |

`no_arguments_bound` 49 → 0 is the substantive change. The parser plans an
explicit zero phrase for an omitted Japanese subject and the analyzer tested
for absence only, so every such sentence finished with an event that had no
arguments at all. That single disagreement also explains the +19 in frame
resolution, and it was found by clustering the loss rather than by reading a
count.

`certified` stays 0. Passing the hard gate and proving equivalence are
different claims and no candidate on this corpus is proved equivalent yet;
the four selected candidates are LOSSY. That is the honest state, and raising
it by loosening the verifier is exactly what must not happen.
