// Command llamapreset generates and checks the llama.cpp router presets in
// this repository.
//
//	llamapreset build              write dist/ and MODELS.md
//	llamapreset build --notes      print release notes to stdout
//	llamapreset measure --missing  fill in absent VRAM measurements
//	llamapreset validate           check every config against AGENTS.md
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/TokenCemetery/llama.cpp-models.ini/internal/preset"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	root, err := preset.Root()
	if err != nil {
		fail(err)
	}

	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "build":
		fs := flag.NewFlagSet("build", flag.ExitOnError)
		notes := fs.Bool("notes", false, "print release notes to stdout instead of writing dist/")
		must(fs.Parse(args))
		if *notes {
			must(preset.Notes(root))
			return
		}
		must(preset.Build(root))

	case "measure":
		fs := flag.NewFlagSet("measure", flag.ExitOnError)
		missing := fs.Bool("missing", false, "fill in whatever is absent")
		quants := fs.Bool("quants", false, "measure quant VRAM at the reference context")
		curves := fs.Bool("context", false, "measure max_ctx and the context ladder")
		jobs := fs.Int("jobs", 8, "parallel measurements")
		must(fs.Parse(args))
		if *missing {
			*quants, *curves = true, true
		}
		if !*quants && !*curves {
			fail(fmt.Errorf("give --quants, --context or --missing"))
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		os.Exit(preset.Measure(ctx, root, preset.MeasureOptions{
			Models: fs.Args(), Quants: *quants, Curves: *curves, Jobs: *jobs,
		}))

	case "validate":
		fs := flag.NewFlagSet("validate", flag.ExitOnError)
		skipKeys := fs.Bool("skip-keys", false, "offline: skip the llama.cpp key check")
		argCpp := fs.String("arg-cpp", "", "path to a local common/arg.cpp")
		must(fs.Parse(args))
		os.Exit(preset.Validate(root, preset.ValidateOptions{SkipKeys: *skipKeys, ArgCpp: *argCpp}))

	case "-h", "--help", "help":
		usage()

	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, strings.TrimLeft(`
usage: llamapreset <command> [flags]

commands:
  build                write dist/ and MODELS.md
  build --notes        print release notes to stdout
  measure --missing    fill in absent VRAM measurements
  measure --quants     measure quant VRAM at the reference context
  measure --context    measure max context and the context ladder
  validate             check every config against the rules in AGENTS.md
  validate --skip-keys  offline: skip the llama.cpp key check
`, "\n"))
}

func must(err error) {
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
