package preset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	gguf "github.com/gpustack/gguf-parser-go"
)

// Measures model VRAM and records it in configs/vram-estimates.json.
//
// Each config declares the HuggingFace repo it came from in a "; repo:"
// comment, so this reads that and measures against the matching GGUF files.
//
// Two things are recorded per model:
//
//	quants      VRAM of every in-band quant at the reference context and KV type.
//	ref_curves  VRAM of one reference quant across a ladder of context sizes,
//	            measured once per KV cache type.
//
// KV cache cost per token depends on the architecture and cache type, not on
// the weight quantisation - measured slopes for UD-Q3_K_XL and Q6_K agree to
// four significant figures. So a quant's whole curve is the matching reference
// curve plus a constant, and build derives any (quant, context, KV type) triple
// as VRAM = ref_curves[kv][ctx] + quants[quant] - quants[ref_quant].
//
// Results are written after each measurement, so an interrupted run loses
// nothing and re-running resumes where it stopped.

const (
	refCtx = 32768  // context the quants table is measured at
	refKV  = "q8_0" // KV type the quants table is measured with
	hfAPI  = "https://huggingface.co/api/models/"

	// The gguf-parser CLI adds a fixed footprint on top of the model's own
	// usage, and the recorded numbers include it. Passing zero here instead
	// silently lowers every figure by 0.24 GiB, which changes which models fit
	// a tier. Verified against the CLI: with these values the library
	// reproduces the committed measurements exactly.
	nonUMARAMFootprint  = 150 * 1024 * 1024
	nonUMAVRAMFootprint = 250 * 1024 * 1024
)

// kvTypes are measured for every model, in this order.
var kvTypes = []struct {
	name string
	typ  gguf.GGMLType
}{
	{"f16", gguf.GGMLTypeF16},
	{"q8_0", gguf.GGMLTypeQ8_0},
	{"q5_1", gguf.GGMLTypeQ5_1},
	{"q4_0", gguf.GGMLTypeQ4_0},
}

var ladder = []int{4096, 8192, 16384, 32768, 65536, 131072, 262144}

// refPreference is the order in which the quant whose context curve gets
// measured is chosen.
var refPreference = []string{"UD-Q4_K_XL", "UD-Q4_K_M", "Q4_K_M", "UD-Q3_K_XL"}

// Kept in step with the "Only use these quants" rule in AGENTS.md.
var measurableQuants = []string{
	"UD-Q3_K_XL", "Q4_K_M", "UD-Q4_K_M", "UD-Q4_K_XL",
	"Q5_K_M", "UD-Q5_K_M", "UD-Q5_K_XL", "Q6_K", "UD-Q6_K",
}

// quantRe matches the quant tag at the end of a GGUF filename. Alternation
// order follows measurableQuants, and Go's regexp is leftmost-first like
// Python's, so "UD-Q4_K_M" wins over the "Q4_K_M" inside it.
var quantRe = regexp.MustCompile(`(?i)(` + strings.Join(measurableQuants, "|") + `)\.gguf$`)

var sidecarPrefixes = []string{"mtp-", "dspark-", "dflash-"}

func kvType(name string) (gguf.GGMLType, bool) {
	for _, k := range kvTypes {
		if k.name == name {
			return k.typ, true
		}
	}
	return 0, false
}

type MeasureOptions struct {
	Models []string
	Quants bool
	Curves bool
	Jobs   int
}

// estimate runs one measurement. ctxSize of 0 means the model's own maximum.
// It returns the context actually used and the VRAM in GiB.
func estimate(ctx context.Context, repo, file string, ctxSize int, kv string) (int, float64, error) {
	typ, ok := kvType(kv)
	if !ok {
		return 0, 0, fmt.Errorf("unknown KV cache type %q", kv)
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ { // range requests to HF are occasionally flaky
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		gf, err := gguf.ParseGGUFFileFromHuggingFace(ctx, repo, file)
		if err != nil {
			lastErr = err
			continue
		}
		opts := []gguf.GGUFRunEstimateOption{
			gguf.WithFlashAttention(),
			gguf.WithLLaMACppCacheKeyType(typ),
			gguf.WithLLaMACppCacheValueType(typ),
		}
		if ctxSize > 0 {
			opts = append(opts, gguf.WithLLaMACppContextSize(int32(ctxSize)))
		} else {
			opts = append(opts, gguf.WithinLLaMACppMaxContextSize())
		}
		e := gf.EstimateLLaMACppRun(opts...)
		s := e.Summarize(true, nonUMARAMFootprint, nonUMAVRAMFootprint)
		if len(s.Items) == 0 || len(s.Items[0].VRAMs) == 0 {
			lastErr = errors.New("estimate returned no VRAM figures")
			continue
		}
		gib := float64(s.Items[0].VRAMs[0].NonUMA) / (1 << 30)
		return int(e.ContextSize), gib, nil
	}
	return 0, 0, lastErr
}

type hfModel struct {
	Siblings []struct {
		RFilename string `json:"rfilename"`
	} `json:"siblings"`
}

// inBandFiles lists the repo's GGUF files whose quant is in our band,
// excluding vision projectors and draft models.
func inBandFiles(ctx context.Context, repo string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hfAPI+repo, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }() // nothing useful to do with a close error
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", repo, resp.Status)
	}
	var m hfModel
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}

	var files []string
	for _, s := range m.Siblings {
		name := s.RFilename
		base := name
		if i := strings.LastIndex(name, "/"); i >= 0 {
			base = name[i+1:]
		}
		lower := strings.ToLower(base)
		if !strings.HasSuffix(name, ".gguf") || !quantRe.MatchString(name) {
			continue
		}
		if strings.Contains(lower, "mmproj") {
			continue
		}
		skip := false
		for _, p := range sidecarPrefixes {
			if strings.HasPrefix(lower, p) {
				skip = true
			}
		}
		if !skip {
			files = append(files, name)
		}
	}
	sort.Strings(files)
	return files, nil
}

func quantOf(filename string) string {
	m := quantRe.FindStringSubmatch(filename)
	if m == nil {
		return ""
	}
	return m[1]
}

func ladderFor(maxCtx int) []int {
	var out []int
	for _, c := range ladder {
		if c < maxCtx {
			out = append(out, c)
		}
	}
	return append(out, maxCtx)
}

type job struct {
	kind     string // "quant" or "curve"
	modelID  string
	repo     string
	filename string
	ctx      int
	kv       string
}

func Measure(ctx context.Context, root string, opts MeasureOptions) int {
	configs, err := LoadModelConfigs(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	repos := map[string]string{}
	var allIDs []string
	for _, cfg := range configs {
		if cfg.Repo == "" {
			fmt.Fprintf(os.Stderr, "%s: no '; repo:' comment, cannot measure it\n", cfg.Path)
			return 1
		}
		repos[cfg.ID] = cfg.Repo
		allIDs = append(allIDs, cfg.ID)
	}

	targets := opts.Models
	if len(targets) == 0 {
		targets = allIDs
	}
	var unknown []string
	for _, id := range targets {
		if _, ok := repos[id]; !ok {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		fmt.Fprintln(os.Stderr, "no config for: "+strings.Join(unknown, ", "))
		return 1
	}

	est, err := LoadEstimates(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	var mu sync.Mutex // guards est and the file
	save := func() {
		if err := est.Save(root); err != nil {
			fmt.Fprintf(os.Stderr, "could not save estimates: %v\n", err)
		}
	}

	filesCache := map[string][]string{}
	var filesMu sync.Mutex
	filesFor := func(id string) ([]string, error) {
		filesMu.Lock()
		defer filesMu.Unlock()
		if f, ok := filesCache[id]; ok {
			return f, nil
		}
		f, err := inBandFiles(ctx, repos[id])
		if err != nil {
			return nil, err
		}
		filesCache[id] = f
		return f, nil
	}

	// ---- pass 1: model max context, and the reference quant ----------------
	if opts.Curves {
		var needMax []string
		for _, id := range targets {
			if d, ok := est.Data[id]; !ok || d.MaxCtx == 0 {
				needMax = append(needMax, id)
			}
		}
		if len(needMax) > 0 {
			fmt.Printf("resolving max context for %d model(s)\n", len(needMax))
			runPool(opts.Jobs, len(needMax), func(i int) {
				id := needMax[i]
				files, err := filesFor(id)
				if err != nil || len(files) == 0 {
					fmt.Printf("  FAILED max_ctx %s\n", id)
					return
				}
				ref := files[0]
				for _, want := range refPreference {
					found := false
					for _, f := range files {
						if quantOf(f) == want {
							ref, found = f, true
							break
						}
					}
					if found {
						break
					}
				}
				maxCtx, gib, err := estimate(ctx, repos[id], ref, 0, refKV)
				if err != nil || maxCtx == 0 {
					fmt.Printf("  FAILED max_ctx %s\n", id)
					return
				}
				mu.Lock()
				est.SetMaxCtx(id, quantOf(ref), maxCtx)
				est.SetCurvePoint(id, refKV, maxCtx, gib)
				save()
				mu.Unlock()
				fmt.Printf("  %s: max_ctx=%d (%s = %s GiB)\n", id, maxCtx, quantOf(ref), formatGiB(gib))
			})
		}
	}

	// ---- pass 2: build the job list ----------------------------------------
	var jobs []job
	for _, id := range targets {
		d, ok := est.Data[id]
		if !ok {
			d = est.Entry(id)
		}
		var files []string
		if opts.Quants {
			files, err = filesFor(id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  could not list %s: %v\n", repos[id], err)
				continue
			}
			for _, f := range files {
				if _, seen := d.Quants[quantOf(f)]; !seen {
					jobs = append(jobs, job{"quant", id, repos[id], f, refCtx, refKV})
				}
			}
		}
		if opts.Curves && d.MaxCtx != 0 && d.RefQuant != "" {
			if files == nil {
				files, err = filesFor(id)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  could not list %s: %v\n", repos[id], err)
					continue
				}
			}
			ref := ""
			for _, f := range files {
				if quantOf(f) == d.RefQuant {
					ref = f
					break
				}
			}
			if ref == "" {
				continue
			}
			for _, kv := range kvTypes {
				for _, c := range ladderFor(d.MaxCtx) {
					have := false
					if curve, ok := d.Curves[kv.name]; ok {
						_, have = curve.Vals[strconv.Itoa(c)]
					}
					if !have {
						jobs = append(jobs, job{"curve", id, repos[id], ref, c, kv.name})
					}
				}
			}
		}
	}

	if len(jobs) == 0 {
		fmt.Println("nothing to measure; everything is already recorded")
		return 0
	}
	fmt.Printf("%d measurement(s) to run\n", len(jobs))

	type outcome struct {
		gib float64
		err error
	}
	results := make([]outcome, len(jobs))
	ready := make([]chan struct{}, len(jobs))
	for i := range ready {
		ready[i] = make(chan struct{})
	}

	go runPool(opts.Jobs, len(jobs), func(i int) {
		j := jobs[i]
		_, gib, err := estimate(ctx, j.repo, j.filename, j.ctx, j.kv)
		results[i] = outcome{gib, err}
		close(ready[i])
	})

	done, failed := 0, 0
	for i, j := range jobs {
		<-ready[i]
		r := results[i]
		if r.err != nil {
			failed++
			fmt.Printf("  FAILED %s %s ctx=%d kv=%s\n", j.modelID, j.filename, j.ctx, j.kv)
			continue
		}
		mu.Lock()
		var label string
		if j.kind == "quant" {
			est.SetQuant(j.modelID, quantOf(j.filename), r.gib)
			label = "quant " + quantOf(j.filename)
		} else {
			est.SetCurvePoint(j.modelID, j.kv, j.ctx, r.gib)
			label = fmt.Sprintf("kv %s ctx %d", j.kv, j.ctx)
		}
		save()
		mu.Unlock()
		done++
		fmt.Printf("  [%d/%d] %s %s = %s GiB\n", done+failed, len(jobs), j.modelID, label, formatGiB(r.gib))
	}

	fmt.Printf("\nmeasured %d, failed %d\n", done, failed)
	if failed > 0 {
		return 1
	}
	return 0
}

// runPool runs fn(0..n-1) with at most workers running concurrently.
//
// Every index is always dispatched, even after the context is cancelled, so
// callers waiting on a per-index signal cannot deadlock. Cancellation is
// handled inside estimate, which returns immediately once ctx is done.
func runPool(workers, n int, fn func(i int)) {
	if workers < 1 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		sem <- struct{}{}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
}
