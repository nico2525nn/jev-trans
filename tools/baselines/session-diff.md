# Corpus baseline

76 sentences from `corpus/akita.txt` (Miyazawa Kenji, 1920s), measured with
`tools/stages.py`. Per-sentence counting is cumulative: a stage counts only when
every earlier stage counted for that sentence, so the rows are a funnel and not
independent samples. Compared against 4eae9af, the session's starting commit.

| metric | 4eae9af | current | delta |
|---|---|---|---|
| morphology fully resolved | 68 | 68 | +0 |
| predicate fully resolved | 25 | 31 | +6 |
| semantic frame fully resolved | 9 | 28 | +19 |
| unresolved clause heads | 70 | 61 | -9 |
| raw candidates | 162 | 211 | +49 |
| eligible (gate passed) | 1 | 3 | +2 |
| certified (equivalence proved) | 0 | 0 | +0 |
| selected | 1 | 3 | +2 |

## Argument losses

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
explicit zero phrase for an omitted Japanese subject and the analyzer tested for
absence only, so every such sentence finished with an event that had no arguments
at all. The argument binder was correct throughout — it was handed a clause that
was already broken — which is why the loss is worth naming: the report pointed at
the binder for a long time before the cause turned out to be upstream of it.
That single disagreement also explains the +19 in frame resolution.

`eligible` went 1 → 4 and then back to 3. The last step down is `道が悪いので野原を歩く」,
whose candidate was *eligible* and translated to "A road goes." — the を-marked
place of motion had been dropped from the source graph, so the gate comparing
target against source agreed on a translation of a different sentence. Losing that
candidate is the intended outcome; `野原` has no English gloss and must stay
unrealized rather than be invented.

`certified` stays 0. Passing the hard gate and proving equivalence are different
claims, no candidate on this corpus is proved equivalent, and the three selected
ones are LOSSY or AMBIGUOUS. Loosening the verifier to move that number would make
every other number here worthless.

## Per backend

`--morph builtin` and `--morph auto` are different systems and the numbers differ.
An earlier version of this file claimed they agreed; that was measured with
`JEV_SUDACHI_ADAPTER=off`, which selects which process adapter `internal/lex`
auto-discovers and is not the builtin backend at all.

| metric | `--morph builtin` | `--morph auto` (Sudachi) |
|---|---|---|
| morphology fully resolved | 3 | 68 |
| predicate fully resolved | 38 | 31 |
| semantic frame fully resolved | 36 | 28 |
| unresolved clause heads | 40 | 61 |
| raw candidates | 173 | 211 |
| eligible (gate passed) | 5 | 3 |
| certified (equivalence proved) | 0 | 0 |
| selected | 5 | 3 |

The builtin analyser resolves more predicates and more frames because it carries
explicit entries for pre-1946 spellings SudachiDict core does not segment in a
form this pipeline can use — 「云ふ」 was unrecognised on the Sudachi path until the
classical 言う forms were registered on both. It resolves far less morphology,
because every surface its dictionary does not list becomes an unknown token.

Neither is strictly better, which is the argument for measuring both and for the
test suite's `forEachBackend` switching the engine's morphological registry rather
than an environment variable.
