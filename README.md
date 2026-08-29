# llama.cpp-models.ini

Ready-to-use [llama.cpp](https://github.com/ggml-org/llama.cpp) router presets, organised by VRAM budget.

The configs contain **no filesystem paths**. Models are located by `--models-dir`, so the same
file works unmodified on any machine — you only name your model folders to match.

Quantisation and context are not guesses: every model was measured with
[gguf-parser](https://github.com/gpustack/gguf-parser-go), and each file gets the largest
context and best quant that fit its budget. Raw numbers are in `configs/vram-estimates.json`.

## Usage

Download the profile for your GPU from the
[latest release](https://github.com/TokenCemetery/llama.cpp-models.ini/releases/latest).
Releases are rebuilt automatically whenever the catalog changes, so this is always current:

```sh
curl -LO https://github.com/TokenCemetery/llama.cpp-models.ini/releases/latest/download/vram-16gb-balanced.ini
```

Then run:

```sh
llama-server \
  --models-dir /home/user/models \
  --models-preset vram-16gb-balanced.ini \
  --host 127.0.0.1 --port 8080
```

`--models-dir` is the single root where all models live on disk.

Each release also ships `SHA256SUMS`. If you prefer to clone, run `python3 src/build.py`
to generate the same files into `dist/`.

## Profiles

| Budget | Models |
| --- | --- |
| 4 GB | 17 |
| 8 GB | 36 |
| 16 GB | 62 |
| 24 GB | 71 |
| 32 GB | 71 |

Each budget ships three profiles, because quantisation and context compete for the same VRAM:

| Profile | Strategy |
| --- | --- |
| `vram-NNgb-quality.ini` | best quant first, then as much context as fits |
| `vram-NNgb-balanced.ini` | at least 64K context, then the best quant, then more context |
| `vram-NNgb-context.ini` | most context first, then the best quant that still fits |

They agree for about 72% of entries and differ where a model can hold more context than its
best quant allows. gemma-3-27b-it at 24 GB is the clearest case: `quality` picks `Q6_K` at 8K
context, `balanced` picks `UD-Q5_K_XL` at 64K, `context` picks `UD-Q3_K_XL` at 128K.

Start with `balanced` unless you know which side you want.


Each file lists its models with the chosen quant, context and estimated VRAM in a header
comment. Estimates assume flash-attention on, `q8_0` K/V cache and full GPU offload, and
reserve 1 GiB for the OS/display.

VRAM alone doesn't determine a working config. Unified-memory devices (Apple Silicon, Steam
Deck, other iGPUs) share that budget with the OS and should use the smaller `uma` figure;
MoE models offloaded with `--n-cpu-moe` depend on system RAM as much as on VRAM.

## Required folder layout

llama.cpp derives a model's name from the **directory name**, and each `[section]` must match
one exactly. The rule used here:

> **directory name = HuggingFace repo name, minus the `-GGUF` suffix, lowercased**

Upstream casing is inconsistent (`Qwen3.5-27B-GGUF` but `gemma-4-31B-it-GGUF` and
`gpt-oss-20b-GGUF`), so everything is normalised to lowercase. Only the *directory* name is
normalised — the `.gguf` files inside keep whatever name they were downloaded with.

```
/home/user/models/
├── gemma-4-e4b-it/
│   ├── gemma-4-E4B-it-Q6_K.gguf
│   ├── mmproj-F16.gguf
│   └── mtp-gemma-4-E4B-it.gguf
├── gpt-oss-20b/
│   └── gpt-oss-20b-Q6_K.gguf
└── qwen3.8-27b/
    └── Qwen3.8-27B-UD-Q3_K_XL.gguf
```

```sh
hf download unsloth/Qwen3.8-27B-GGUF \
    --include "*UD-Q3_K_XL*" \
    --local-dir /home/user/models/qwen3.8-27b
```

**Matching is case-sensitive** — llama.cpp compares the section name against the directory name
byte-for-byte. This bites on macOS and Windows: the filesystem is case-insensitive, so creating
`Qwen3.8-27B/` "works", but the directory is reported under that exact name and will not match
the `[qwen3.8-27b]` section. Create the directories in lowercase.

Use one subdirectory per model — even for single-file models — so the model id never leaks the
quantisation. The *same* section then works across tiers with a different GGUF inside.

**`--models-dir` does not recurse.** A nested `<provider>/<model>/` tree does not work: the
provider directory is either registered as a model under the wrong name, or skipped silently.
Keep model directories exactly one level below the root.

Rules llama.cpp applies inside a model directory:

| File | Treated as |
| --- | --- |
| filename contains `mmproj` | vision projector, wired to `--mmproj` automatically |
| filename starts with `mtp-`, `dspark-`, `dflash-` | speculative-decoding draft model |
| filename contains `-00001-of-` | first shard of a multi-part GGUF |
| any other `.gguf` | the model itself |

**Put exactly one model `.gguf` per directory.** If a directory holds two, llama.cpp silently
keeps whichever comes last in directory order.

### Models you haven't downloaded

A preset may list models you don't have; the server still starts and lists them. But requesting
one spawns a child process that fails, and the request hangs until your client gives up
(cleanup then takes `stop-timeout` seconds). Delete the sections you don't intend to use.

## Repo layout

```
configs/<provider>/<model>.ini   one section per model, tuning only, no paths
configs/vram-estimates.json      gguf-parser measurements: quants and context curves
src/measure.py                   runs gguf-parser, writes vram-estimates.json
src/build.py                     generates dist/ from the two above
src/validate.py                  checks every config against the repo rules
dist/                            generated presets (git-ignored, published as releases)
```

llama.cpp's preset format has no `include` directive, so tier files are generated by
concatenation rather than composed at runtime.

To add a model: drop a file in `configs/<provider>/` including a `; repo:` comment, then run:

```sh
python3 src/measure.py --missing   # runs gguf-parser, records the numbers
python3 src/build.py               # regenerates dist/
```

Sampling defaults come from the model authors via the
[Unsloth model guides](https://unsloth.ai/docs/models/tutorials); each config cites its source.
Quants are restricted to the `UD-Q3_K_XL` .. `Q6_K` band.

## Writing presets

Keys are llama.cpp command-line arguments without the leading dashes; short forms (`c`, `ngl`)
and env-var names (`LLAMA_ARG_N_GPU_LAYERS`) also work. `[*]` holds shared defaults that any
model section can override.

Settings are applied in this order, each overriding the previous:

1. models discovered in the HF cache
2. models discovered under `--models-dir`
3. the preset file
4. **arguments passed to `llama-server` on the command line**

Step 4 catches people out: a flag on the router command line overrides the preset for *every*
model, so don't pass tuning flags there unless you mean it globally.

Three keys do **not** work inside a preset — the router strips or overwrites them before
launching each model process. Pass them to `llama-server` instead:

- `host` and `port` — overwritten with the child process's own address
- `api-key` — stripped entirely

This is verified behaviour, not a guess: with `api-key` set in `[*]`, the server logs
`no API key is set` and serves requests unauthenticated. If you bind to `0.0.0.0`, set
`--api-key` on the command line.

## Compatibility

Router mode and the preset format are recent additions and still changing. Verified against
llama.cpp `0.3.0` (build 10621, `c1d0e7a00`). Check your build:

```sh
llama-server --help | grep models-preset
```

Note that build warns the default port will change to `:9931` in a future release — pass
`--port` explicitly rather than relying on the default.
