#!/usr/bin/env python3
"""Measure model VRAM with gguf-parser and record it in configs/vram-estimates.json.

Each config declares the HuggingFace repo it came from in a "; repo:" comment,
so this reads that and measures against the matching GGUF files.

Two things are recorded per model:

  quants      VRAM of every in-band quant at the reference context and KV type.
  ref_curves  VRAM of one reference quant across a ladder of context sizes,
              measured once per KV cache type.

KV cache cost per token depends on the architecture and cache type, not on the
weight quantisation - measured slopes for UD-Q3_K_XL and Q6_K agree to four
significant figures. So a quant's whole curve is the matching reference curve
plus a constant, and build.py derives any (quant, context, KV type) triple as:

    VRAM = ref_curves[kv][ctx] + quants[quant] - quants[ref_quant]

Results are written after each measurement, so an interrupted run loses nothing
and re-running resumes where it stopped.

Usage:
    python3 src/measure.py --missing              # fill in whatever is absent
    python3 src/measure.py --quants qwen3-8b      # quant VRAM at 32768
    python3 src/measure.py --context qwen3-8b     # max_ctx + context ladder
"""

import argparse
import json
import pathlib
import re
import subprocess
import sys
import urllib.request
from concurrent.futures import ThreadPoolExecutor

ROOT = pathlib.Path(__file__).resolve().parent.parent
CONFIGS = ROOT / "configs"
ESTIMATES = CONFIGS / "vram-estimates.json"

# Kept in step with the "Only use these quants" rule in AGENTS.md.
ALLOWED = ["UD-Q3_K_XL", "Q4_K_M", "UD-Q4_K_M", "UD-Q4_K_XL",
           "Q5_K_M", "UD-Q5_K_M", "UD-Q5_K_XL", "Q6_K", "UD-Q6_K"]
QUANT_RE = re.compile(r"(" + "|".join(ALLOWED) + r")\.gguf$", re.I)
SIDECAR = ("mtp-", "dspark-", "dflash-")

REF_CTX = 32768
REF_KV = "q8_0"                                  # KV type used for the quants table
KV_TYPES = ["f16", "q8_0", "q5_1", "q4_0"]       # curves are measured for each
LADDER = [4096, 8192, 16384, 32768, 65536, 131072, 262144]
# Preference order when choosing the quant whose context curve we measure.
REF_PREFERENCE = ["UD-Q4_K_XL", "UD-Q4_K_M", "Q4_K_M", "UD-Q3_K_XL"]


def model_repos():
    """{model_id: hf_repo} taken from each config's '; repo:' comment."""
    out = {}
    for path in sorted(CONFIGS.rglob("*.ini")):
        text = path.read_text()
        sec = re.search(r"^\[([^\]]+)\]", text, re.M)
        repo = re.search(r"^;\s*repo:\s*(\S+)", text, re.M)
        if not sec:
            sys.exit(f"{path}: no [section] found")
        if not repo:
            sys.exit(f"{path}: no '; repo:' comment, cannot measure it")
        out[sec.group(1).strip()] = repo.group(1).strip()
    return out


def in_band_files(repo):
    data = json.load(urllib.request.urlopen(
        f"https://huggingface.co/api/models/{repo}", timeout=60))
    files = []
    for sib in data.get("siblings", []):
        name = sib["rfilename"]
        base = name.split("/")[-1]
        if (name.endswith(".gguf") and QUANT_RE.search(name)
                and "mmproj" not in base.lower()
                and not base.lower().startswith(SIDECAR)):
            files.append(name)
    return sorted(files)


def quant_of(filename):
    return QUANT_RE.search(filename).group(1)


def _run(repo, filename, ctx, kv=REF_KV):
    """Return (max_ctx, vram_gib) or (None, None). ctx=0 means the model's max."""
    cmd = ["gguf-parser", "--hf-repo", repo, "--hf-file", filename,
           "--ctx-size", str(ctx), "--flash-attention",
           "--cache-type-k", kv, "--cache-type-v", kv,
           "--gpu-layers", "-1", "--estimate", "--json"]
    for _ in range(3):  # range requests to HF are occasionally flaky
        try:
            proc = subprocess.run(cmd, capture_output=True, text=True, timeout=300)
            est = json.loads(proc.stdout)["estimate"]
            return est["contextSize"], round(est["items"][0]["vrams"][0]["nonuma"] / 2 ** 30, 2)
        except Exception:
            pass
    return None, None


def measure(repo, filename, ctx):
    """Back-compat helper: VRAM at an explicit context."""
    _, gib = _run(repo, filename, ctx)
    return gib, None if gib is not None else "failed"


def load():
    return json.loads(ESTIMATES.read_text())


def save(est):
    ESTIMATES.write_text(json.dumps(est, indent=2) + "\n")


def entry(est, model_id):
    return est["models"].setdefault(
        model_id, {"max_ctx": None, "ref_quant": None, "ref_curves": {}, "quants": {}})


def ladder_for(max_ctx):
    steps = [c for c in LADDER if c < max_ctx]
    return steps + [max_ctx]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("models", nargs="*", help="model ids (default: all)")
    ap.add_argument("--missing", action="store_true", help="fill in whatever is absent")
    ap.add_argument("--quants", action="store_true", help="measure quant VRAM at 32768")
    ap.add_argument("--context", action="store_true", help="measure max_ctx and the ladder")
    ap.add_argument("--jobs", type=int, default=8, help="parallel measurements")
    args = ap.parse_args()

    if args.missing:
        args.quants = args.context = True
    if not (args.quants or args.context):
        ap.error("give --quants, --context or --missing")

    repos = model_repos()
    targets = args.models or list(repos)
    unknown = [m for m in targets if m not in repos]
    if unknown:
        sys.exit("no config for: " + ", ".join(unknown))

    est = load()
    files_cache = {}

    def files_for(model_id):
        if model_id not in files_cache:
            files_cache[model_id] = in_band_files(repos[model_id])
        return files_cache[model_id]

    # ---- pass 1: model max context, and the reference quant ---------------
    need_max = [m for m in targets if not entry(est, m)["max_ctx"]] if args.context else []
    if need_max:
        print(f"resolving max context for {len(need_max)} model(s)", flush=True)

        def get_max(model_id):
            files = files_for(model_id)
            ref = next((f for q in REF_PREFERENCE for f in files if quant_of(f) == q), files[0])
            max_ctx, gib = _run(repos[model_id], ref, 0)
            return model_id, quant_of(ref), max_ctx, gib

        with ThreadPoolExecutor(max_workers=args.jobs) as pool:
            for model_id, quant, max_ctx, gib in pool.map(get_max, need_max):
                if not max_ctx:
                    print(f"  FAILED max_ctx {model_id}", flush=True); continue
                est = load()
                e = entry(est, model_id)
                e["max_ctx"] = max_ctx
                e["ref_quant"] = quant
                e["ref_curves"].setdefault(REF_KV, {})[str(max_ctx)] = gib
                save(est)
                print(f"  {model_id}: max_ctx={max_ctx} ({quant} = {gib} GiB)", flush=True)

    # ---- pass 2: build the job list ---------------------------------------
    jobs = []
    est = load()
    for model_id in targets:
        e = entry(est, model_id)
        files = None
        if args.quants:
            files = files_for(model_id)
            for f in files:
                if quant_of(f) not in e["quants"]:
                    jobs.append(("quant", model_id, f, REF_CTX, REF_KV))
        if args.context and e["max_ctx"] and e["ref_quant"]:
            files = files or files_for(model_id)
            ref = next(f for f in files if quant_of(f) == e["ref_quant"])
            for kv in KV_TYPES:
                for c in ladder_for(e["max_ctx"]):
                    if str(c) not in e["ref_curves"].get(kv, {}):
                        jobs.append(("curve", model_id, ref, c, kv))

    if not jobs:
        print("nothing to measure; everything is already recorded")
        return 0
    print(f"{len(jobs)} measurement(s) to run", flush=True)

    def run(job):
        kind, model_id, filename, ctx, kv = job
        _, gib = _run(repos[model_id], filename, ctx, kv)
        return kind, model_id, filename, ctx, kv, gib

    done = failed = 0
    with ThreadPoolExecutor(max_workers=args.jobs) as pool:
        for kind, model_id, filename, ctx, kv, gib in pool.map(run, jobs):
            if gib is None:
                failed += 1
                print(f"  FAILED {model_id} {filename} ctx={ctx} kv={kv}", flush=True)
                continue
            est = load()
            e = entry(est, model_id)
            if kind == "quant":
                e["quants"][quant_of(filename)] = gib
                e["quants"] = dict(sorted(e["quants"].items(), key=lambda i: i[1]))
                label = f"quant {quant_of(filename)}"
            else:
                curve = e["ref_curves"].setdefault(kv, {})
                curve[str(ctx)] = gib
                e["ref_curves"][kv] = dict(sorted(curve.items(), key=lambda i: int(i[0])))
                label = f"kv {kv} ctx {ctx}"
            save(est)
            done += 1
            print(f"  [{done + failed}/{len(jobs)}] {model_id} {label} = {gib} GiB", flush=True)

    print(f"\nmeasured {done}, failed {failed}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
