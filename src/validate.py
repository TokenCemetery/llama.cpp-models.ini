#!/usr/bin/env python3
"""Check every config in this repo against the rules in AGENTS.md.

Exits 0 if everything passes, 1 if anything fails. Every failure prints the
file, the line, and what to do about it.

Usage:
    python3 src/validate.py                 # all checks (downloads arg.cpp once)
    python3 src/validate.py --skip-keys     # offline: skip the llama.cpp key check
    python3 src/validate.py --arg-cpp PATH  # use a local copy of arg.cpp
"""

import argparse
import json
import pathlib
import re
import sys
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent
CONFIGS = ROOT / "configs"
DIST = ROOT / "dist"
ESTIMATES = CONFIGS / "vram-estimates.json"

ARG_CPP_URL = "https://raw.githubusercontent.com/ggml-org/llama.cpp/master/common/arg.cpp"
CACHE = pathlib.Path("/tmp/llama-cpp-arg.cpp")

MIN_CTX = 4096
FORBIDDEN_KEYS = {"host", "port", "api-key"}
# KV cache types the profiles in build.py need a measured curve for.
NEEDED_KV = {"f16", "q8_0", "q5_1", "q4_0"}
ALLOWED_QUANTS = {
    "UD-Q3_K_XL", "Q4_K_M", "UD-Q4_K_M", "UD-Q4_K_XL",
    "Q5_K_M", "UD-Q5_K_M", "UD-Q5_K_XL", "Q6_K", "UD-Q6_K",
}

failures = []


def fail(where, msg, fix):
    failures.append(f"{where}\n    problem: {msg}\n    fix:     {fix}")


def parse_ini(path):
    """Yield (line_number, section, key, value). Section is None before the first header."""
    section = None
    for n, raw in enumerate(path.read_text().splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith((";", "#")):
            continue
        if line.startswith("["):
            section = line.strip("[]")
            yield n, section, None, None
            continue
        if "=" in line:
            key, value = line.split("=", 1)
            yield n, section, key.strip(), value.strip()


def load_valid_keys(args):
    if args.skip_keys:
        return None
    if args.arg_cpp:
        text = pathlib.Path(args.arg_cpp).read_text()
    elif CACHE.exists():
        text = CACHE.read_text()
    else:
        try:
            text = urllib.request.urlopen(ARG_CPP_URL, timeout=60).read().decode()
            CACHE.write_text(text)
        except Exception as e:
            print(f"could not download arg.cpp ({e}); re-run with --skip-keys", file=sys.stderr)
            sys.exit(2)

    keys = {k.lstrip("-") for k in re.findall(r'"(--?[A-Za-z0-9][A-Za-z0-9._-]*)"', text)}
    keys |= set(re.findall(r'set_env\("([A-Z0-9_]+)"\)', text))
    marker = "void common_params_add_preset_options"
    if marker in text:
        keys |= set(re.findall(r'\{"([a-z][a-z0-9-]*)"\}', text[text.index(marker):]))
    return keys


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--skip-keys", action="store_true", help="skip the llama.cpp key check")
    ap.add_argument("--arg-cpp", help="path to a local common/arg.cpp")
    args = ap.parse_args()

    valid_keys = load_valid_keys(args)
    estimates = json.loads(ESTIMATES.read_text())["models"]
    configs = sorted(CONFIGS.rglob("*.ini"))

    if not configs:
        fail("configs/", "no config files found", "add configs/<provider>/<model>.ini")

    seen_ids = {}

    for path in configs:
        rel = path.relative_to(ROOT)
        sections = [s for _, s, k, _ in parse_ini(path) if k is None]

        if len(sections) != 1:
            fail(f"{rel}", f"expected exactly 1 section, found {len(sections)}",
                 "one model per file; build.py writes the [*] section itself")
            continue

        model_id = sections[0]

        if model_id == "*":
            fail(f"{rel}", "contains a [*] section",
                 "remove it; build.py writes [*] into dist/ files")
        if model_id != model_id.lower():
            fail(f"{rel}", f"model id '{model_id}' is not lowercase",
                 f"rename the section and file to '{model_id.lower()}'")
        if path.stem != model_id:
            fail(f"{rel}", f"file name '{path.stem}' does not match section '{model_id}'",
                 f"rename the file to '{model_id}.ini' (use git mv)")
        if model_id in seen_ids:
            fail(f"{rel}", f"model id '{model_id}' also defined in {seen_ids[model_id]}",
                 "model ids must be unique")
        seen_ids[model_id] = rel
        if model_id not in estimates:
            fail(f"{rel}", f"no VRAM data for '{model_id}' in configs/vram-estimates.json",
                 "measure it with gguf-parser and add it; see AGENTS.md")

        for n, _, key, value in parse_ini(path):
            if key is None:
                continue
            if key in FORBIDDEN_KEYS:
                fail(f"{rel}:{n}", f"'{key}' does nothing in a config file",
                     f"remove it and pass --{key} on the llama-server command line")
            if valid_keys is not None and key not in valid_keys:
                fail(f"{rel}:{n}", f"llama.cpp does not accept the key '{key}'",
                     "the server would exit with code 1; check the spelling against arg.cpp")
            if ";" in value or "#" in value:
                fail(f"{rel}:{n}", f"value for '{key}' contains ';' or '#'",
                     "llama.cpp silently deletes the rest of the line; remove the character")
            if value.startswith("/") or value.startswith("~") or "{" in key:
                fail(f"{rel}:{n}", f"'{key}' looks like a file path",
                     "configs must not contain paths; models come from --models-dir")
            if key in ("model", "mmproj", "model-draft", "hf-repo"):
                fail(f"{rel}:{n}", f"'{key}' hardcodes a model location",
                     "remove it; llama.cpp finds the files via --models-dir")
            if key == "ctx-size":
                # Optional. Present means "never give this model more than N",
                # which build.py respects; absent means "as much as fits".
                if not value.isdigit() or int(value) < MIN_CTX:
                    fail(f"{rel}:{n}", f"ctx-size cap '{value}' is not a number >= {MIN_CTX}",
                         "remove it to let build.py choose, or set a sensible cap")

    where = "configs/vram-estimates.json"
    for model_id, data in estimates.items():
        if model_id not in seen_ids:
            fail(where, f"'{model_id}' has VRAM data but no config file",
                 f"add configs/<provider>/{model_id}.ini or remove the entry")
        for quant in data.get("quants", {}):
            if quant not in ALLOWED_QUANTS:
                fail(where, f"'{model_id}' uses quant '{quant}'",
                     f"allowed quants: {', '.join(sorted(ALLOWED_QUANTS))}")
        if not data.get("quants"):
            fail(where, f"'{model_id}' has no quant measurements",
                 f"run: python3 src/measure.py --quants {model_id}")
        curves = data.get("ref_curves", {})
        missing_kv = sorted(NEEDED_KV - set(curves))
        if not curves or not data.get("max_ctx"):
            fail(where, f"'{model_id}' has no context curves",
                 f"run: python3 src/measure.py --context {model_id}")
        elif missing_kv:
            fail(where, f"'{model_id}' has no curve for KV type(s): {', '.join(missing_kv)}",
                 f"run: python3 src/measure.py --context {model_id}")
        elif data.get("ref_quant") not in data.get("quants", {}):
            fail(where, f"'{model_id}' ref_quant '{data.get('ref_quant')}' is missing from quants",
                 "the context curves cannot be offset without it; re-measure the model")
        else:
            for kv, curve in curves.items():
                if str(data["max_ctx"]) not in curve:
                    fail(where, f"'{model_id}' {kv} curve has no point at max_ctx {data['max_ctx']}",
                         f"run: python3 src/measure.py --context {model_id}")

    if not DIST.exists() or not list(DIST.glob("*.ini")):
        fail("dist/", "no generated files found", "run: python3 src/build.py")

    if failures:
        print(f"FAILED - {len(failures)} problem(s)\n")
        for f in failures:
            print(f"  {f}\n")
        return 1

    print(f"OK - {len(configs)} configs, {len(estimates)} models, "
          f"{len(list(DIST.glob('*.ini')))} generated files")
    return 0


if __name__ == "__main__":
    sys.exit(main())
