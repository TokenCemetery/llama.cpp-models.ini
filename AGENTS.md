# AGENTS.md

This file tells coding agents how to work in this repository. Read it before changing any file.

## What this repo is

This repo stores llama.cpp router presets, grouped by VRAM size.

- You edit files in `configs/`.
- You run `python3 src/build.py`.
- It writes files to `dist/`.

There is no application code. There are no unit tests.

## Rules

Follow these. They are not style preferences. Breaking them breaks the config.

1. Never edit files in `dist/`. They are generated. Edit `configs/` and run `python3 src/build.py`.
2. Always run `python3 src/build.py` after changing `configs/` or `configs/vram-estimates.json`.
3. Model IDs are lowercase. Example: `qwen3.8-27b`, not `Qwen3.8-27B`.
4. A config file's name must match its section name.
   File `configs/unsloth/qwen3.8-27b.ini` must contain `[qwen3.8-27b]`.
5. Never put a file path in a config. No `model = /path/...` line. Paths come from `--models-dir`.
   There is also no way to set a base folder inside the file. Config files have no variables.
   `{my-path}` is not replaced with anything. It stays as the literal text `{my-path}`.
6. Keep `ctx-size = 32768` in every config. The VRAM numbers were measured at that value. Changing it makes every number wrong.
7. Only use these quants: `UD-Q3_K_XL`, `Q4_K_M`, `UD-Q4_K_M`, `UD-Q4_K_XL`, `Q5_K_M`, `UD-Q5_K_M`, `UD-Q5_K_XL`, `Q6_K`.
8. Never put `host`, `port`, or `api-key` in a config. They do not work. See "Traps".
9. Never put `;` or `#` inside a value. It silently deletes the rest of the line.
10. Do not add a `[*]` section to files in `configs/`. `src/build.py` writes `[*]` itself.
11. Do not repeat a value in a model section if `[*]` already sets it.
12. Never commit `dist/`. It is generated and git ignores it. Only commit `configs/` and `src/`.
13. Write commit messages as Conventional Commits: `feat:`, `fix:`, `docs:`, `refactor:`, `ci:`.
    Release notes are built from them by git-cliff. A commit in any other format is left out
    of the notes.

## Commands

Rebuild `dist/`:

```sh
python3 src/build.py
```

Check every config against the rules in this file:

```sh
python3 src/validate.py
```

Measure a model's VRAM. This reads the file header over the network. It does not download the model:

```sh
gguf-parser --hf-repo unsloth/Qwen3.8-27B-GGUF --hf-file Qwen3.8-27B-UD-Q4_K_XL.gguf \
            --ctx-size 32768 --flash-attention \
            --cache-type-k q8_0 --cache-type-v q8_0 \
            --gpu-layers -1 --estimate --json
```

Read the number at `estimate.items[0].vrams[0].nonuma`. It is in bytes. Divide by 1073741824 to get GiB.
Put that number in `configs/vram-estimates.json`.

Test that a config file works:

```sh
llama-server --models-dir /path/to/models --models-preset dist/vram-16gb.ini --no-models-autoload
```

- Exit code 0 and the server starts = the file is good.
- Exit code 1 = the file is broken. Read the error message. It names the bad key.

This test takes about 6 seconds. You do not need any models on disk to run it.

## How to add a model

1. Create `configs/<provider>/<model-id>.ini`.
2. Write the section as `[<model-id>]`. Use the same name as the file.
3. Add sampling settings. Copy them from the model author's docs. Add a comment saying where they came from.
4. Measure each quant with `gguf-parser`. Add the results to `configs/vram-estimates.json` under `<model-id>`.
5. Run `python3 src/build.py`.
6. Test with `llama-server`.

If you skip step 4, `src/build.py` stops with an error.

## How the build works

```
configs/<provider>/<model>.ini   you edit these
configs/vram-estimates.json      you edit this
        |
        v
src/build.py                     you run this
        |
        v
dist/vram-<NN>gb.ini             do not edit these
```

For each VRAM size, `src/build.py` picks the best quant that fits in `size - 1 GiB`. It then copies in every model that fits.

The build step exists because llama.cpp config files cannot include other files. There is no `include` keyword. So the files must be joined together before llama.cpp sees them.

## How llama.cpp finds models

You start the server with `--models-dir /path/to/models`.

llama.cpp reads the folder names inside that path. Each folder name becomes a model name.
A section in your config file applies to a model when the section name and the folder name are **exactly the same**.

Correct layout:

```
/path/to/models/
├── gemma-4-e4b-it/
│   ├── gemma-4-E4B-it-Q6_K.gguf
│   ├── mmproj-F16.gguf
│   └── mtp-gemma-4-E4B-it.gguf
└── qwen3.8-27b/
    └── Qwen3.8-27B-UD-Q3_K_XL.gguf
```

Rules for the folder:

| File name | llama.cpp treats it as |
| --- | --- |
| contains `mmproj` | vision model, loaded automatically |
| starts with `mtp-`, `dspark-`, `dflash-` | draft model, loaded automatically |
| contains `-00001-of-` | first part of a split model |
| any other `.gguf` | the model |

- Put exactly one model `.gguf` in each folder. With two, llama.cpp picks one at random and says nothing.
- Model folders must be one level deep. `/path/to/models/unsloth/qwen3.8-27b/` does **not** work. llama.cpp does not look inside sub-folders.

## Traps

Each of these fails silently or confusingly. They were all tested on a real server.

**`host`, `port`, `api-key` do nothing in a config file.**
llama.cpp deletes them before starting a model. Tested: set `api-key = secret` in `[*]`, and the server still printed `no API key is set` and answered requests with no key. Pass them on the command line instead:

```sh
llama-server --models-preset dist/vram-16gb.ini --host 127.0.0.1 --port 8080 --api-key secret
```

**A `;` or `#` inside a value deletes the rest of the line.**
Tested: `chat-template-kwargs = {"a":"x;y","b":"z"}` reached the model as `{"a":"x`. That is broken JSON, and there is no warning.

**Folder names are case-sensitive, even on macOS and Windows.**
A folder named `Qwen3.8-27B` will not match a section named `[qwen3.8-27b]`. Your Mac lets you create it. llama.cpp still refuses to match it. Always create folders in lowercase.

**Never rename a file by writing the new name then deleting the old one.**
On macOS and Windows, `Foo.ini` and `foo.ini` are the same file. Writing then deleting destroys it.
Use `git mv` instead. This bug already deleted 15 files in this repo once.

**A command-line flag beats the config file.**
Settings are applied in this order, last one wins:

1. models found in the HuggingFace cache
2. models found in `--models-dir`
3. the config file
4. flags typed on the `llama-server` command line

So `llama-server --temp 0` sets temperature 0 for every model and ignores your config.

**A model listed in the config but missing from disk does not cause an error.**
The server starts normally. The request just hangs until your client gives up. Then cleanup takes 30 seconds.
If you do not have a model, delete its section.

**`load-on-startup` takes a model name, not `true` or `false`.**
To stop models loading at startup, use the flag `--no-models-autoload`.

**Unsloth docs say `repetition_penalty`. llama.cpp does not accept that name.**
Use `repeat-penalty`.

## Config file format

Keys are llama.cpp command-line flags without the leading dashes.

| Command line | Config file |
| --- | --- |
| `--n-gpu-layers 99` | `n-gpu-layers = 99` |
| `--ctx-size 32768` | `ctx-size = 32768` |
| `--flash-attn auto` | `flash-attn = auto` |

Short names like `c` and `ngl` also work. So do environment variable names like `LLAMA_ARG_N_GPU_LAYERS`.

If you use a key that llama.cpp does not know, the server exits with code 1 and prints the bad key name.

To check a key without starting the server, download llama.cpp's list of flags:

```sh
curl -sfL https://raw.githubusercontent.com/ggml-org/llama.cpp/master/common/arg.cpp -o /tmp/arg.cpp
grep -n '"--your-key"' /tmp/arg.cpp
```

Some keys only exist in config files, not on the command line. They are listed in `arg.cpp` inside `common_params_add_preset_options`. Right now they are `load-on-startup`, `stop-timeout`, and `dedup-cache-models`.

## Before you finish

Run these two commands. Both must succeed:

```sh
python3 src/build.py
python3 src/validate.py
```

`validate.py` checks every rule in this file. If it fails, it prints the file, the line, the
problem, and the fix. Fix the problems and run it again. Do not stop while it still fails.

If you have no network access, use `python3 src/validate.py --skip-keys`. That skips only the
check for unknown llama.cpp keys.

GitHub Actions runs these same two commands on every pull request. A push to `main` that changes
`configs/` or `src/` builds `dist/` and publishes it as a new release.

## Version note

This was checked against llama.cpp `0.3.0`, build 10621, commit `c1d0e7a00`.

Router mode is new and still changing. If something here does not match, trust the running server, not this file. Check that the feature exists at all with:

```sh
llama-server --help | grep models-preset
```

Details of the behaviour above come from `common/preset.cpp`, `common/arg.cpp`, and `tools/server/server-models.cpp` in the llama.cpp source.
