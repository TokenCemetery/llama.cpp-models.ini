# llama.cpp models.ini

Ready-to-use [llama.cpp](https://github.com/ggml-org/llama.cpp) router presets, so you can run
71 models from one server without hand-tuning a config.

Pick the file that matches your GPU, put your models in one folder, and start the server. There
are no filesystem paths inside the presets, so the same file works on any machine.

## Quick start

**1. Download the preset for your GPU.**

```sh
curl -LO https://github.com/TokenCemetery/llama.cpp-models.ini/releases/latest/download/vram-16gb.ini
```

Replace `16gb` with your VRAM. Releases are rebuilt whenever the catalogue changes, so `latest`
is always current. Each release also ships `SHA256SUMS`.

**2. Pick a line from the table at the top of the file.**

Each line is one ready-made setup — a model at a particular quantisation, context length and KV
cache precision, all of which fit your card:

```
;   qwen3.8-27b-ud-q3_k_xl-32k-q8_0    balanced    14.2 GiB
```

**3. Download that quant into a folder with exactly that name.**

The folder name *is* the setup. llama.cpp matches folders under `--models-dir` against section
names in the preset, so naming the folder correctly is what applies the settings:

```sh
hf download unsloth/Qwen3.8-27B-GGUF \
    --include "*UD-Q3_K_XL*" \
    --local-dir /home/user/models/qwen3.8-27b-ud-q3_k_xl-32k-q8_0
```

**4. Start the server.**

```sh
llama-server \
  --models-dir /home/user/models \
  --models-preset vram-16gb.ini \
  --host 127.0.0.1 --port 8080
```

Models load on demand when you request them. The preset lists every setup that fits your card,
so **delete the sections you don't intend to download** — see
[Troubleshooting](#troubleshooting).

## Which file to pick

One file per card. There is no profile suffix — all three profiles live inside each file.

| Your GPU | File | Models | Setups |
| --- | --- | --- | --- |
| 4 GB | `vram-04gb.ini` | 22 | 50 |
| 8 GB | `vram-08gb.ini` | 36 | 92 |
| 16 GB | `vram-16gb.ini` | 62 | 157 |
| 24 GB | `vram-24gb.ini` | 71 | 176 |
| 32 GB | `vram-32gb.ini` | 71 | 158 |

## Which setup to pick

Quantisation, context length and KV cache precision all compete for the same VRAM. Each model
appears once per sensible way to spend it, labelled with the profile it suits:

| Profile | Best for |
| --- | --- |
| `quality` | Short chats where answer quality matters most. Lossless `f16` KV cache. |
| `balanced` | **Start here.** At least 64K context with near-lossless `q8_0` KV cache. |
| `context` | Long documents and agent loops. Trades KV precision down to `q4_0` for context. |

Where a model already reaches its maximum context on your card the profiles agree, and you get
one line covering all three. Otherwise they diverge sharply — Qwen3.8-27B on a 24 GB card spans
an 8x range in context:

| Profile | Folder name to use | Quant to download |
| --- | --- | --- |
| `quality` | `qwen3.8-27b-ud-q6_k-32k-f16` | `UD-Q6_K` |
| `balanced` | `qwen3.8-27b-ud-q5_k_xl-64k-q8_0` | `UD-Q5_K_XL` |
| `context` | `qwen3.8-27b-ud-q4_k_xl-256k-q4_0` | `UD-Q4_K_XL` |

You can keep more than one at a time — they are separate folders, so they show up as separate
models and you choose per request. That costs disk space, since each holds its own copy of the
weights.

To change your mind later, rename the folder to the name of the setup you want instead. Nothing
inside the folder needs to change unless the quantisation differs.

### A note on VRAM figures

The numbers assume a discrete GPU with flash-attention on and the whole model on the GPU, and
they leave 1 GiB free for your desktop.

Two cases need care. **Unified-memory devices** — Apple Silicon, Steam Deck, other iGPUs —
share that budget with the operating system, so treat the tier as optimistic. **MoE models**
offloaded with `--n-cpu-moe` depend on system RAM as much as VRAM.

### Looking for a specific model?

[MODELS.md](MODELS.md) lists all 71 with parameter counts and capabilities. A few to orient you:

| Model | Params | Good at | Smallest card |
| --- | --- | --- | --- |
| `qwen3-4b` | 4B | reasoning | 4 GB |
| `gemma-3-4b-it` | 4B | vision | 8 GB |
| `qwen3-coder-30b-a3b-instruct` | 30B-A3B | moe, coding | 16 GB |
| `gpt-oss-20b` | 20B | moe, reasoning | 16 GB |
| `qwen3.8-27b` | 27B | vision, reasoning | 16 GB |
| `qwq-32b` | 32B | reasoning | 24 GB |

## Setting up your models folder

`--models-dir` points at one folder. Each setup gets its own subfolder directly inside it:

```
/home/user/models/
├── gemma-4-e4b-it-q6_k-128k-f16/
│   ├── gemma-4-E4B-it-Q6_K.gguf
│   ├── mmproj-F16.gguf
│   └── mtp-gemma-4-E4B-it.gguf
├── gpt-oss-20b-q6_k-128k-f16/
│   └── gpt-oss-20b-Q6_K.gguf
└── qwen3.8-27b-ud-q5_k_xl-64k-q8_0/
    └── Qwen3.8-27B-UD-Q5_K_XL.gguf
```

Only the **folder** name has to match the preset. The `.gguf` files inside keep whatever name
they were downloaded with — note the folder name is lowercase while the file usually is not.

The folder name is also the model name you send to the API:

```sh
curl http://127.0.0.1:8080/v1/chat/completions \
  -d '{"model":"qwen3.8-27b-ud-q5_k_xl-64k-q8_0","messages":[{"role":"user","content":"hi"}]}'
```

If that is too long to type, give it a short name with `alias` in the preset:

```ini
[qwen3.8-27b-ud-q5_k_xl-64k-q8_0]
alias = qwen
```

llama.cpp picks up extra files in the folder automatically:

| File | What it does |
| --- | --- |
| name contains `mmproj` | vision support, loaded automatically |
| name starts with `mtp-`, `dspark-`, `dflash-` | speculative decoding, for faster output |
| name contains `-00001-of-` | first part of a split model |
| any other `.gguf` | the model itself |

Two rules that will bite you if you break them:

- **One model `.gguf` per folder.** With two, llama.cpp silently picks whichever comes last.
- **Folders go one level deep.** `models/unsloth/qwen3.8-27b-.../` does not work — llama.cpp does
  not look inside subfolders, so the model simply won't appear.

## Editing the preset

The file is plain INI. Keys are llama.cpp command-line flags without the leading dashes, so
`--temp 0.6` becomes `temp = 0.6`. The `[*]` section holds defaults for every model.

Delete any `[section]` whose folder you don't have. Most of the file will be sections you never
download — a 24 GB preset offers 176 setups across 71 models, and you only need the handful you
actually want.

To use settings other than the ones a section was generated with, just edit them. Nothing checks
that `ctx-size` still matches the number in the section name; the name is only how llama.cpp
pairs the section with your folder.

Three settings **do not work** inside the file, because the router overrides them before
starting each model. Pass them to `llama-server` instead:

```sh
llama-server --models-preset vram-16gb.ini \
             --host 0.0.0.0 --port 8080 --api-key your-secret
```

- `host` and `port` are replaced with the model process's own address
- `api-key` is removed entirely

This matters for security: putting `api-key` in the file looks like it works, but the server
starts with **no authentication** and logs `no API key is set`. If you bind to `0.0.0.0`, pass
`--api-key` on the command line.

Also note that any flag you type on the `llama-server` command line **overrides the preset for
every model**. `llama-server --temp 0` sets temperature 0 everywhere, ignoring the file.

## Troubleshooting

**My model is listed but ignores the settings — wrong context, wrong KV cache.**
The folder name doesn't exactly match a section name, so llama.cpp registered the folder on its
own and there was no section to merge in. Matching is exact and case-sensitive: a folder called
`Qwen3.8-27B-UD-Q3_K_XL-32K-Q8_0` will not match `[qwen3.8-27b-ud-q3_k_xl-32k-q8_0]`. macOS and
Windows let you create it anyway, because their filesystems ignore case, but llama.cpp still
won't match it. Copy the name from the preset rather than typing it. Also check the folder is
directly under `--models-dir`, not nested.

**A request hangs for a long time, then fails.**
You asked for a model listed in the preset that isn't on disk. The server starts a process for
it, that process fails, and your client waits until it gives up. Delete the sections you aren't
using.

**The server exits immediately with an error naming a key.**
Your llama.cpp is older than the preset. Update it, or remove that line.

**Everything runs but uses the wrong settings.**
Check for flags on your `llama-server` command line — they beat the file.

## How these files are made

Quantisation and context are not guesses. Every model is measured with
[gguf-parser](https://github.com/gpustack/gguf-parser-go) across context sizes and KV cache
types, and each preset gets the best combination that fits its budget.

Sampling settings come from each model author's published guidance, via the
[Unsloth model guides](https://unsloth.ai/docs/models/tutorials). Every model file cites its
source.

Quantisations are limited to the `UD-Q3_K_XL` to `Q6_K` range — below that quality drops off
sharply, above it the size rarely justifies the gain.

Want to add a model or change a setting? See [AGENTS.md](AGENTS.md) for how the repo is built.

## Compatibility

Router mode and the preset format are recent additions to llama.cpp and still changing. These
files are verified against llama.cpp `0.3.0` (build 10621, `c1d0e7a00`).

Check your build supports it:

```sh
llama-server --help | grep models-preset
```

If that prints nothing, update llama.cpp.

One upstream warning worth heeding: the default port is changing to `:9931` in a future
release, so pass `--port` explicitly rather than relying on the default.
