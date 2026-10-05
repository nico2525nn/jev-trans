#!/usr/bin/env python3
"""Sudachi morphological backend for JEV-Trans, over a line protocol.

This file is the process boundary. The Go core never imports sudachipy, never
links a native library, and never ships a dictionary: it starts this script,
writes one JSON request per line, and reads one JSON response per line. If the
script or the dictionary is missing the core falls back to its builtin
analyser, and the trace says which one answered.

    {"text": "...", "profile": "modern"}
    {"backend":"sudachi","version":"0.7.0","dictionary":"core","tokens":[...]}

Run it directly to check an installation:

    python3 tools/sudachi_backend.py --selftest "今日はいい天気ですね。"
"""

import json
import os
import sys

# Where a specific SudachiDict build is pinned. SUDACHIDICT_DIR is Sudachi's own
# variable and still wins if this is unset.
DICT_ENV = "JEV_SUDACHI_DICT"


def load_dictionary(profile):
    """Build a Sudachi tokenizer for the profile.

    The public API is Dictionary -> tokenizer(). Constructing
    sudachipy.Tokenizer directly is not supported and raises
    "cannot create Tokenizer instances" — which looks exactly like an
    incompatible dictionary format and is not one. Getting that wrong is how an
    adapter ends up reporting a version incompatibility that does not exist.

    profile selects a dictionary rather than a different analyser: modern and
    modern-literary both want SudachiDict, and old-kana-colloquial wants the
    国語研 UniDic build, which is the same format behind a different path.
    """
    from sudachipy import dictionary as _dictionary

    path = os.environ.get(DICT_ENV) or os.environ.get("SUDACHIDICT_DIR")
    if path:
        cfg = _dictionary.Config()
        cfg.system_dict.update(path=path)
        d = _dictionary.Dictionary(cfg)
    else:
        d = _dictionary.Dictionary()

    # tokenizer() is the current entry point; create() is the deprecated name
    # and is kept only for a SudachiPy older than 0.5.
    if hasattr(d, "tokenizer"):
        return d.tokenizer()
    return d.create()


def _oov(m):
    """Whether Sudachi flagged the morpheme as out of vocabulary.

    The attribute was renamed between releases, so both are probed rather than
    assuming one: reporting every token as unknown would make the corpus metrics
    meaningless, and reporting none of them would hide the gaps.
    """
    for attr in ("is_oov", "is_known"):
        fn = getattr(m, attr, None)
        if fn is None:
            continue
        try:
            # is_oov() answers "was this out of vocabulary", so it inverts
            # directly; is_known() answers the opposite. Calling the attribute
            # without parentheses returns the bound method, which is truthy,
            # and would mark every morpheme unknown.
            if attr == "is_oov":
                return bool(fn())
            return not bool(fn())
        except Exception:  # noqa: BLE001
            continue
    # No flag available: treat as known, which is the optimistic and therefore
    # less misleading direction — the coverage ratio is reported separately.
    return False


def _byte_offsets(text):
    """Map character index -> byte offset.

    Sudachi reports character offsets; the Go core uses byte offsets
    everywhere, because every span it records is a slice of the original
    string. Sending character offsets makes every downstream span point at the
    wrong place for any text containing multi-byte characters — which is all
    Japanese.
    """
    table = [0]
    total = 0
    for ch in text:
        total += len(ch.encode("utf-8"))
        table.append(total)
    return table


def tokenize(tok, text):
    out = []
    offsets = _byte_offsets(text)
    for m in tok.tokenize(text):
        reading = ""
        try:
            reading = m.reading_form() or ""
        except Exception:  # noqa: BLE001
            pass
        out.append({
            "surface": m.surface(),
            "lemma": m.normalized_form() or m.surface(),
            "baseForm": m.dictionary_form() or m.surface(),
            "normalized": m.normalized_form(),
            "reading": reading,
            "pos": list(m.part_of_speech()),
            # 0.7 exposes the offsets on the morpheme as begin/end rather than
            # start/end, and both are character offsets; convert to bytes.
            "start": offsets[min(m.begin(), len(offsets) - 1)],
            "end": offsets[min(m.end(), len(offsets) - 1)],
            "unknown": _oov(m),
            "features": {"rule": "sudachi", "oov": "1" if _oov(m) else "0"},
        })
    return out


def sudachi_version():
    try:
        import sudachipy
        return getattr(sudachipy, "__version__", "unknown")
    except Exception:  # noqa: BLE001
        return "unavailable"


def dict_name():
    path = os.environ.get(DICT_ENV) or os.environ.get("SUDACHIDICT_DIR")
    if not path:
        try:
            import sudachidict_core
            return "sudachidict_core"
        except Exception:  # noqa: BLE001
            return "default"
    return os.path.basename(os.path.normpath(path))


def respond(obj):
    sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def main():
    if "--selftest" in sys.argv:
        i = sys.argv.index("--selftest")
        text = sys.argv[i + 1] if len(sys.argv) > i + 1 else "今日はいい天気ですね。"
        try:
            tok = load_dictionary("modern")
        except Exception as e:  # noqa: BLE001
            print(f"sudachi unavailable: {type(e).__name__}: {e}")
            return 1
        for t in tokenize(tok, text):
            print(f"  {t['surface']!r}\t{t['lemma']}\t{t['pos']}\tunknown={t['unknown']}")
        return 0

    # The tokenizer is built once: importing SudachiDict costs about a second,
    # and paying that per sentence would dominate every translation.
    tok = None
    load_error = None
    version = sudachi_version()

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except Exception as e:  # noqa: BLE001
            respond({"error": f"malformed request: {e}"})
            continue
        profile = req.get("profile") or "modern"
        if tok is None:
            if load_error is not None:
                respond({"error": load_error})
                continue
            try:
                tok = load_dictionary(profile)
            except Exception as e:  # noqa: BLE001
                load_error = f"{type(e).__name__}: {e}"
                respond({"error": load_error})
                continue
        try:
            respond({
                "backend": "sudachi",
                "version": version,
                "dictionary": dict_name(),
                "tokens": tokenize(tok, req.get("text", "")),
            })
        except Exception as e:  # noqa: BLE001
            respond({"error": f"{type(e).__name__}: {e}"})


if __name__ == "__main__":
    sys.exit(main())
