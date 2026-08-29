#!/usr/bin/env python3
"""Measure model VRAM with gguf-parser and record it in configs/vram-estimates.json.

Each config declares the HuggingFace repo it came from in a "; repo:" comment,
so this reads that and measures every quant in the allowed band.

Results are written after each measurement, so an interrupted run loses nothing
and re-running picks up where it stopped.

Usage:
    python3 src/measure.py --missing            # every model with no data yet
    python3 src/measure.py qwen3-8b gemma-3-4b-it
    python3 src/measure.py --missing --jobs 8
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
    url = f"https://huggingface.co/api/models/{repo}"
    data = json.load(urllib.request.urlopen(url, timeout=60))
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


def measure(repo, filename, ctx):
    cmd = ["gguf-parser", "--hf-repo", repo, "--hf-file", filename,
           "--ctx-size", str(ctx), "--flash-attention",
           "--cache-type-k", "q8_0", "--cache-type-v", "q8_0",
           "--gpu-layers", "-1", "--estimate", "--json"]
    last = "unknown"
    for _ in range(3):  # range requests to HF are occasionally flaky
        try:
            proc = subprocess.run(cmd, capture_output=True, text=True, timeout=300)
            item = json.loads(proc.stdout)["estimate"]["items"][0]
            return round(item["vrams"][0]["nonuma"] / 2 ** 30, 2), None
        except Exception as exc:
            last = type(exc).__name__
    return None, last


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("models", nargs="*", help="model ids to measure")
    ap.add_argument("--missing", action="store_true",
                    help="measure every quant not yet recorded, for every model")
    ap.add_argument("--jobs", type=int, default=6, help="parallel measurements")
    args = ap.parse_args()

    est = json.loads(ESTIMATES.read_text())
    ctx = est["_meta"]["params"]["ctx_size"]
    repos = model_repos()

    if args.missing:
        # Every model: the per-quant filter below decides what actually runs, so
        # a run interrupted halfway through a model resumes correctly.
        targets = list(repos)
    elif args.models:
        targets = args.models
        unknown = [m for m in targets if m not in repos]
        if unknown:
            sys.exit("no config for: " + ", ".join(unknown))
    else:
        ap.error("give model ids or --missing")

    if not targets:
        print("nothing to measure; every model already has data")
        return 0

    print(f"measuring {len(targets)} model(s) at ctx-size {ctx}", flush=True)

    jobs = []
    for model_id in targets:
        for filename in in_band_files(repos[model_id]):
            if quant_of(filename) not in est["models"].get(model_id, {}):
                jobs.append((model_id, repos[model_id], filename))
    print(f"{len(jobs)} quant(s) to measure", flush=True)

    done = failed = 0

    def run(job):
        model_id, repo, filename = job
        gib, err = measure(repo, filename, ctx)
        return model_id, quant_of(filename), gib, err

    with ThreadPoolExecutor(max_workers=args.jobs) as pool:
        for model_id, quant, gib, err in pool.map(run, jobs):
            if gib is None:
                failed += 1
                print(f"  FAILED {model_id} {quant}: {err}", flush=True)
                continue
            # Re-read so a concurrent edit is not clobbered, then write through.
            est = json.loads(ESTIMATES.read_text())
            est["models"].setdefault(model_id, {})[quant] = gib
            est["models"][model_id] = dict(sorted(
                est["models"][model_id].items(), key=lambda kv: kv[1]))
            ESTIMATES.write_text(json.dumps(est, indent=2) + "\n")
            done += 1
            print(f"  [{done + failed}/{len(jobs)}] {model_id} {quant} = {gib} GiB", flush=True)

    print(f"\nmeasured {done}, failed {failed}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
