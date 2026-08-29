# src/

The tool that generates this repo's presets. One Go binary, three subcommands.

Read this before changing any file in here. If you only want to **add a model**, you do not
need this file at all — see [AGENTS.md](../AGENTS.md).

## Commands

Use the [Makefile](../Makefile) in the repository root. Run `make` for the list:

```sh
make check      # test + build + validate. Run this before committing
make build      # writes dist/ and MODELS.md
make validate   # checks every rule in AGENTS.md
make measure    # fills in absent VRAM numbers. MODEL=<id> for one model
make notes      # release body, to stdout
make fmt vet    # format, then report suspicious constructs
```

The module lives in `src/`, so every target is a `go -C src` command underneath. Call the tool
directly when you need a flag the Makefile does not expose:

```sh
go -C src run ./cmd/llamapreset validate --skip-keys   # offline
go -C src run ./cmd/llamapreset measure --jobs 4       # gentler on HuggingFace
go -C src test ./... -run RoundTrip -v
```

`cd src` and dropping the `-C src` works too. The tool finds the repository root by walking up
from the working directory, so it does not care where you run it from.

## Layout

```
cmd/llamapreset/main.go      flag parsing and dispatch
internal/preset/config.go    reads configs/**/*.ini
internal/preset/estimates.go reads and writes configs/vram-estimates.json
internal/preset/jsonorder.go order-preserving JSON, so rewrites stay byte-stable
internal/preset/plan.go      picks the (quant, context, KV) triple per profile
internal/preset/render.go    writes dist/*.ini, MODELS.md and the release notes
internal/preset/measure.go   VRAM measurement via gguf-parser-go
internal/preset/validate.go  the rules in AGENTS.md
```

## The two things that break silently

Everything else in here fails loudly. These two do not.

### 1. The footprint constants

`measure.go` calls `Summarize(mmap, ramFootprint, vramFootprint)` and **must** pass
150 MiB and 250 MiB.

Those are the defaults the `gguf-parser` command-line tool applied to every number already
recorded in `configs/vram-estimates.json`. Passing `0` still returns a believable figure — just
0.24 GiB lower, for every model. That is enough to change which models fit which VRAM tier, and
nothing anywhere reports an error.

The check: delete a recorded number, run `measure` again, and confirm the same number comes
back. If it does not, do not commit the result.

### 2. Key order in the estimates file

`configs/vram-estimates.json` is committed, so reading and writing it must not change a byte.
`encoding/json` would break that twice over:

- it sorts object keys alphabetically
- it writes a whole number as `5` where the file has `5.0` (8 values in the file are affected)

Key order is not decoration. `quants` is sorted by VRAM ascending, and `plan.go` walks it in
that order. Picks take the **first** maximal candidate, so re-sorting that map would quietly
change which quant some models get.

`jsonorder.go` therefore keeps insertion order and stores every number as the literal text it
was read as. `go -C src test ./...` asserts the file round-trips byte-identically. If that test
fails, the bug is in `jsonorder.go`, not in the estimates.

## Verifying a change

The output is the contract. Before and after any change to `plan.go` or `render.go`:

```sh
mkdir -p /tmp/before && cp dist/*.ini MODELS.md /tmp/before/
# ... make your change ...
make build
for f in dist/*.ini; do diff /tmp/before/$(basename $f) $f; done
diff /tmp/before/MODELS.md MODELS.md
```

An intentional change shows only the lines you meant to change. Anything else — a reordered
section, a shifted VRAM figure — means an ordering assumption broke. The usual cause is
iterating a Go map directly instead of a sorted or file-ordered slice.

## Notes

- Go maps iterate in random order. Never range over one where the result reaches `dist/`.
  Sort the keys, or keep a separate slice of them in the order you need.
- `plan.go` reproduces Python's `max()`, which returns the **first** maximal element. The
  `greater()` helper is strict for that reason; making it `>=` would silently change picks.
- Measurement is network-bound, not CPU-bound. `--jobs` controls how many GGUF headers are
  fetched at once; the default of 8 is polite to HuggingFace.
- `measure` saves after every single measurement, so an interrupted run loses nothing and
  re-running resumes where it stopped.
