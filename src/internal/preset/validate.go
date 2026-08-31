package preset

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Checks every config in this repo against the rules in AGENTS.md.
// Every failure prints the file, the line, and what to do about it.

const (
	argCppURL   = "https://raw.githubusercontent.com/ggml-org/llama.cpp/master/common/arg.cpp"
	argCppCache = "/tmp/llama-cpp-arg.cpp"
)

var (
	forbiddenKeys = []string{"host", "port", "api-key"}
	// KV cache types the profiles in plan.go need a measured curve for.
	neededKV = []string{"f16", "q8_0", "q5_1", "q4_0"}
	// The band is QuantLadder in plan.go.
	allowedQuants = QuantLadder
	pathLikeKeys  = []string{"model", "mmproj", "model-draft", "hf-repo"}
)

type ValidateOptions struct {
	SkipKeys bool
	ArgCpp   string
}

type failure struct{ where, problem, fix string }

type checker struct {
	root     string
	failures []failure
}

func (c *checker) fail(where, problem, fix string) {
	c.failures = append(c.failures, failure{where, problem, fix})
}

func Validate(root string, opts ValidateOptions) int {
	c := &checker{root: root}

	validKeys, err := loadValidKeys(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v; re-run with --skip-keys\n", err)
		return 2
	}

	est, err := LoadEstimates(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	configs, err := LoadModelConfigs(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if len(configs) == 0 {
		c.fail("configs/", "no config files found", "add configs/<provider>/<model>.ini")
	}

	c.checkConfigs(configs, est, validKeys)
	c.checkEstimates(configs, est)
	c.checkDist()

	if len(c.failures) > 0 {
		fmt.Printf("FAILED - %d problem(s)\n\n", len(c.failures))
		for _, f := range c.failures {
			fmt.Printf("  %s\n    problem: %s\n    fix:     %s\n\n", f.where, f.problem, f.fix)
		}
		return 1
	}
	fmt.Printf("OK - %d configs, %d models, %d generated files\n",
		len(configs), len(est.IDs), len(distFiles(root)))
	return 0
}

func (c *checker) checkConfigs(configs []*ModelConfig, est *Estimates, validKeys map[string]bool) {
	seen := map[string]string{}

	for _, cfg := range configs {
		raw, err := os.ReadFile(filepath.Join(c.root, cfg.Path))
		if err != nil {
			c.fail(cfg.Path, err.Error(), "make the file readable")
			continue
		}
		text := string(raw)

		var sections []string
		for _, e := range ParseIni(text) {
			if e.Key == "" {
				sections = append(sections, e.Section)
			}
		}
		if len(sections) != 1 {
			c.fail(cfg.Path, fmt.Sprintf("expected exactly 1 section, found %d", len(sections)),
				"one model per file; build writes the [*] section itself")
			continue
		}
		id := sections[0]

		if id == "*" {
			c.fail(cfg.Path, "contains a [*] section", "remove it; build writes [*] into dist/ files")
		}
		if id != strings.ToLower(id) {
			c.fail(cfg.Path, fmt.Sprintf("model id '%s' is not lowercase", id),
				fmt.Sprintf("rename the section and file to '%s'", strings.ToLower(id)))
		}
		stem := strings.TrimSuffix(filepath.Base(cfg.Path), ".ini")
		if stem != id {
			c.fail(cfg.Path, fmt.Sprintf("file name '%s' does not match section '%s'", stem, id),
				fmt.Sprintf("rename the file to '%s.ini' (use git mv)", id))
		}
		if prev, ok := seen[id]; ok {
			c.fail(cfg.Path, fmt.Sprintf("model id '%s' also defined in %s", id, prev),
				"model ids must be unique")
		}
		seen[id] = cfg.Path
		if _, ok := est.Data[id]; !ok {
			c.fail(cfg.Path, fmt.Sprintf("no VRAM data for '%s' in configs/vram-estimates.json", id),
				"run: make measure")
		}

		head := text
		if i := strings.Index(text, "["+id+"]"); i >= 0 {
			head = text[:i]
		}
		for _, f := range []struct{ field, example string }{
			{"repo", "unsloth/Qwen3-8B-GGUF"}, {"params", "8B"},
		} {
			if headField(head, f.field) == "" {
				c.fail(cfg.Path, fmt.Sprintf("no '; %s:' comment", f.field),
					fmt.Sprintf("add a line like '; %s: %s'; MODELS.md is built from it", f.field, f.example))
			}
		}

		for _, e := range ParseIni(text) {
			if e.Key == "" {
				continue
			}
			at := fmt.Sprintf("%s:%d", cfg.Path, e.Line)
			if contains(forbiddenKeys, e.Key) {
				c.fail(at, fmt.Sprintf("'%s' does nothing in a config file", e.Key),
					fmt.Sprintf("remove it and pass --%s on the llama-server command line", e.Key))
			}
			if validKeys != nil && !validKeys[e.Key] {
				c.fail(at, fmt.Sprintf("llama.cpp does not accept the key '%s'", e.Key),
					"the server would exit with code 1; check the spelling against arg.cpp")
			}
			if strings.ContainsAny(e.Value, ";#") {
				c.fail(at, fmt.Sprintf("value for '%s' contains ';' or '#'", e.Key),
					"llama.cpp silently deletes the rest of the line; remove the character")
			}
			if strings.HasPrefix(e.Value, "/") || strings.HasPrefix(e.Value, "~") || strings.Contains(e.Key, "{") {
				c.fail(at, fmt.Sprintf("'%s' looks like a file path", e.Key),
					"configs must not contain paths; models come from --models-dir")
			}
			if contains(pathLikeKeys, e.Key) {
				c.fail(at, fmt.Sprintf("'%s' hardcodes a model location", e.Key),
					"remove it; llama.cpp finds the files via --models-dir")
			}
			if e.Key == "ctx-size" {
				// Optional. Present means "never give this model more than N",
				// which build respects; absent means "as much as fits".
				n, err := strconv.Atoi(e.Value)
				if err != nil || n < MinCtx {
					c.fail(at, fmt.Sprintf("ctx-size cap '%s' is not a number >= %d", e.Value, MinCtx),
						"remove it to let build choose, or set a sensible cap")
				}
			}
		}
	}
}

func (c *checker) checkEstimates(configs []*ModelConfig, est *Estimates) {
	const where = "configs/vram-estimates.json"

	haveConfig := map[string]bool{}
	for _, cfg := range configs {
		haveConfig[cfg.ID] = true
	}

	for _, id := range est.IDs {
		d := est.Data[id]
		if !haveConfig[id] {
			c.fail(where, fmt.Sprintf("'%s' has VRAM data but no config file", id),
				fmt.Sprintf("add configs/<provider>/%s.ini or remove the entry", id))
		}
		for _, quant := range d.QuantKeys {
			if !contains(allowedQuants, quant) {
				sorted := append([]string(nil), allowedQuants...)
				sort.Strings(sorted)
				c.fail(where, fmt.Sprintf("'%s' uses quant '%s'", id, quant),
					"allowed quants: "+strings.Join(sorted, ", "))
			}
		}
		if len(d.QuantKeys) == 0 {
			c.fail(where, fmt.Sprintf("'%s' has no quant measurements", id),
				"run: make measure MODEL="+id)
		}

		var missingKV []string
		for _, kv := range neededKV {
			if _, ok := d.Curves[kv]; !ok {
				missingKV = append(missingKV, kv)
			}
		}
		sort.Strings(missingKV)

		switch {
		case len(d.Curves) == 0 || d.MaxCtx == 0:
			c.fail(where, fmt.Sprintf("'%s' has no context curves", id),
				"run: make measure MODEL="+id)
		case len(missingKV) > 0:
			c.fail(where, fmt.Sprintf("'%s' has no curve for KV type(s): %s", id, strings.Join(missingKV, ", ")),
				"run: make measure MODEL="+id)
		case !contains(d.QuantKeys, d.RefQuant):
			c.fail(where, fmt.Sprintf("'%s' ref_quant '%s' is missing from quants", id, d.RefQuant),
				"the context curves cannot be offset without it; re-measure the model")
		default:
			for _, kv := range d.KVKeys {
				if _, ok := d.Curves[kv].Vals[strconv.Itoa(d.MaxCtx)]; !ok {
					c.fail(where, fmt.Sprintf("'%s' %s curve has no point at max_ctx %d", id, kv, d.MaxCtx),
						"run: make measure MODEL="+id)
				}
			}
		}
	}
}

func (c *checker) checkDist() {
	files := distFiles(c.root)
	if len(files) == 0 {
		c.fail("dist/", "no generated files found", "run: make build")
		return
	}
	// A repeated section name makes llama.cpp refuse to start with
	// "model 'x' appears multiple times", so catch it before a release does.
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		count := map[string]int{}
		for _, e := range ParseIni(string(raw)) {
			if e.Key == "" && e.Section != "*" {
				count[e.Section]++
			}
		}
		var dupes []string
		for name, n := range count {
			if n > 1 {
				dupes = append(dupes, name)
			}
		}
		sort.Strings(dupes)
		for _, d := range dupes {
			c.fail(rel(c.root, path), fmt.Sprintf("section '%s' appears more than once", d),
				"two setups produced the same name; fix CtxSlug() in src/internal/preset/plan.go")
		}
	}
}

func distFiles(root string) []string {
	files, _ := filepath.Glob(filepath.Join(root, "dist", "*.ini"))
	sort.Strings(files)
	return files
}

var (
	flagRe   = regexp.MustCompile(`"(--?[A-Za-z0-9][A-Za-z0-9._-]*)"`)
	envRe    = regexp.MustCompile(`set_env\("([A-Z0-9_]+)"\)`)
	presetRe = regexp.MustCompile(`\{"([a-z][a-z0-9-]*)"\}`)
)

// loadValidKeys returns the set of keys llama.cpp accepts, or nil when the
// check is skipped.
func loadValidKeys(opts ValidateOptions) (map[string]bool, error) {
	if opts.SkipKeys {
		return nil, nil
	}
	var text string
	switch {
	case opts.ArgCpp != "":
		raw, err := os.ReadFile(opts.ArgCpp)
		if err != nil {
			return nil, err
		}
		text = string(raw)
	default:
		if raw, err := os.ReadFile(argCppCache); err == nil {
			text = string(raw)
			break
		}
		client := &http.Client{Timeout: 60 * time.Second}
		resp, err := client.Get(argCppURL)
		if err != nil {
			return nil, fmt.Errorf("could not download arg.cpp (%w)", err)
		}
		defer func() { _ = resp.Body.Close() }() // nothing useful to do with a close error
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("could not download arg.cpp (status %s)", resp.Status)
		}
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("could not download arg.cpp (%w)", err)
		}
		text = string(raw)
		_ = os.WriteFile(argCppCache, raw, 0o644)
	}

	keys := map[string]bool{}
	for _, m := range flagRe.FindAllStringSubmatch(text, -1) {
		keys[strings.TrimLeft(m[1], "-")] = true
	}
	for _, m := range envRe.FindAllStringSubmatch(text, -1) {
		keys[m[1]] = true
	}
	// Some keys exist only in config files, not on the command line.
	const marker = "void common_params_add_preset_options"
	if i := strings.Index(text, marker); i >= 0 {
		for _, m := range presetRe.FindAllStringSubmatch(text[i:], -1) {
			keys[m[1]] = true
		}
	}
	return keys, nil
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
