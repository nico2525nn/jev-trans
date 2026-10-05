#!/usr/bin/env python3
"""Sudachi morphological backend for JEV-Trans, over a line protocol.

This file is the process boundary. The Go core never imports sudachipy, never
links a native library, and never ships a dictionary: it starts this script,
writes one JSON request per line, and reads one JSON response per line. If the
script or the dictionary is missing the core falls back to its builtin
analyser, and the trace says so.

    {"text": "...", "profile": "modern"}
    {"backend":"sudachi","version":"0.8.2","dictionary":"core","tokens":[...]}

Run it directly to check an installation:

    python3 tools/sudachi_backend.py --selftest "今日はいい天気ですね。"
"""

import json
import os
import sys

# The dictionary search paths SudachiDict uses. SUDACHIDICT_DIR wins so an
# operator can point at a specific build, which matters because SudachiDict
# ships Small, Core and Full and the analysis differs between them.
DICT_ENV = "JEV_SUDACHI_DICT"


def load_dictionary(profile):
    """Build a Sudachi tokenizer, or explain why it cannot be built.

    profile maps onto a dictionary choice rather than onto a different analyser:
    modern and modern-literary both want SudachiDict, and old-kana-colloquial
    wants the 国語研 UniDic build, which is a different dictionary in the same
    format and is configured through the same environment variable.
    """
    import sudachipy  # noqa: E402
    from sudachipy import dictionary as _dictionary  # noqa: E402

    path = os.environ.get(DICT_ENV) or os.environ.get("SUDACHIDICT_DIR")

    # Newer shape: Config moved under the dictionary submodule and the tokenizer
    # takes the built dictionary.
    if hasattr(_dictionary, "Config"):
        cfg = _dictionary.Config()
        if path:
            cfg.system_dict.update(path=path)
        return sudachipy.Tokenizer(_dictionary.Dictionary(cfg))

    # Older shape: Config at the package root, Tokenizer takes no arguments.
    if hasattr(sudachipy, "Config"):
        cfg = sudachipy.Config()
        if path:
            cfg.system_dict.update(path=path)
        return sudachipy.Tokenizer(_dictionary.Dictionary())

    # Oldest shape: the tokenizer wants (dictionary, offset, mode) and the
    # package-level Tokenizer is only the type, not a constructor.
    try:
        return _dictionary.Dictionary()
    except TypeError:
        pass
    try:
        return sudachipy.Tokenizer(_dictionary.Dictionary(), 0, sudachipy.SplitMode.C)
    except Exception:  # noqa: BLE001
        pass
    raise RuntimeError("no usable sudachipy tokenizer constructor was found")


def tokenize(tok, text):
    out = []
    for m in tok.tokenize(text):
        surfaces, _ = m.surface(), m.dictionary_form()
        out.append({
            "surface": m.surface(),
            "lemma": m.normalized_form() or m.surface(),
            "baseForm": m.dictionary_form() or m.surface(),
            "normalized": m.normalized_form(),
            "reading": (m.reading_form() or ""),
            "pos": list(m.part_of_speech()),
            "start": m.start(),
            "end": m.end(),
            # Sudachi's own OOV flag, kept so the trace can distinguish a
            # dictionary hit from a character-category guess.
            "unknown": bool(m.is_known() is False),
            "features": {"rule": "sudachi"},
        })
    return out


def sudachi_version():
    try:
        import sudachipy
        return getattr(sudachipy, "__version__", "unknown")
    except Exception:
        return "unavailable"


def dict_name():
    path = os.environ.get(DICT_ENV) or os.environ.get("SUDACHIDICT_DIR")
    if not path:
        return "default"
    base = os.path.basename(os.path.normpath(path))
    return base


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
            print(f"  {t['surface']!r}\t{t['lemma']}\t{t['pos']}\t{t['unknown']}")
        return 0

    # The tokenizer is built once: importing SudachiDict costs about a second, and
    # paying that per sentence would dominate every translation.
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
