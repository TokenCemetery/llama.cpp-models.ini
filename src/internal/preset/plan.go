package preset

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

var Tiers = []int{4, 8, 16, 24, 32}

const (
	HeadroomGiB = 1.0  // left free for the OS / display
	MinCtx      = 4096 // below this a model is not worth shipping
	BalancedCtx = 65536
)

// KVFidelity is highest fidelity first. f16 is lossless; q4_0 is the cheapest
// we will ship.
var KVFidelity = []string{"f16", "q8_0", "q5_1", "q4_0"}

type Profile struct {
	Name  string
	KV    []string
	Blurb string
}

// Profiles is ordered, and the order reaches dist/: it decides which profile
// names a merged section lists first, and therefore how sections sort.
var Profiles = []Profile{
	{"quality", []string{"f16", "q8_0"},
		"lossless KV cache, best quantisation, then as much context as fits"},
	{"balanced", []string{"q8_0"},
		"q8_0 KV cache, at least 64K context, then the best quantisation"},
	{"context", []string{"q8_0", "q5_1", "q4_0"},
		"most context first, trading KV cache precision down to q4_0 to get it"},
}

func profileIndex(name string) int {
	for i, p := range Profiles {
		if p.Name == name {
			return i
		}
	}
	return len(Profiles)
}

// NeededKV is every KV type some profile can choose, so build can refuse to run
// on incomplete measurements.
func NeededKV() map[string]bool {
	out := map[string]bool{}
	for _, p := range Profiles {
		for _, kv := range p.KV {
			out[kv] = true
		}
	}
	return out
}

// Setup is one model at one (quant, context, KV) triple, with every profile
// that chose it.
type Setup struct {
	*ModelConfig
	KV       string
	Quant    string
	Ctx      int
	GiB      float64
	Profiles []string
	Name     string // section name, which is also the required directory name
}

type candidate struct {
	kv    string
	quant string
	ctx   int
	gib   float64
}

// candidates lists every triple that fits the budget.
func candidates(d *ModelData, budget float64, ctxCap int, kvAllowed []string) []candidate {
	var out []candidate
	for _, kv := range kvAllowed {
		curve, ok := d.Curves[kv]
		if !ok || len(curve.Ladder) == 0 {
			continue
		}
		ladder := curve.Ladder
		if ctxCap > 0 {
			ladder = nil
			for _, c := range curve.Ladder {
				if c <= ctxCap {
					ladder = append(ladder, c)
				}
			}
		}
		if len(ladder) == 0 {
			continue
		}
		top := ladder[len(ladder)-1]
		// QuantKeys is the file's order, and it decides ties further down.
		for _, quant := range d.QuantKeys {
			for _, ctx := range ladder {
				if ctx < MinCtx && ctx != top {
					continue
				}
				gib, ok := d.VRAM(quant, ctx, kv)
				if !ok || gib > budget {
					continue
				}
				out = append(out, candidate{kv, quant, ctx, gib})
			}
		}
	}
	return out
}

// pick chooses one candidate for a profile, or reports none fits.
//
// Every choice is "first maximal wins", matching Python's max(), so candidate
// order is part of the result.
func pick(d *ModelData, budget float64, profile Profile, ctxCap int) (candidate, bool) {
	opts := candidates(d, budget, ctxCap, profile.KV)
	if len(opts) == 0 {
		return candidate{}, false
	}

	// Quant rank: cheapest first, ties keeping file order.
	ranked := append([]string(nil), d.QuantKeys...)
	sort.SliceStable(ranked, func(i, j int) bool { return d.Quants[ranked[i]] < d.Quants[ranked[j]] })
	qrank := make(map[string]int, len(ranked))
	for i, q := range ranked {
		qrank[q] = i
	}
	krank := make(map[string]int, len(KVFidelity))
	for i, kv := range KVFidelity {
		krank[kv] = len(KVFidelity) - i // higher is better fidelity
	}

	best := func(in []candidate, key func(candidate) [3]int) candidate {
		winner := in[0]
		wk := key(winner)
		for _, c := range in[1:] {
			if ck := key(c); greater(ck, wk) {
				winner, wk = c, ck
			}
		}
		return winner
	}

	switch profile.Name {
	case "quality":
		return best(opts, func(c candidate) [3]int {
			return [3]int{krank[c.kv], qrank[c.quant], c.ctx}
		}), true
	case "context":
		return best(opts, func(c candidate) [3]int {
			return [3]int{c.ctx, qrank[c.quant], krank[c.kv]}
		}), true
	}

	// balanced: reach the target context first, then buy quality with the rest.
	maxCtx := opts[0].ctx
	for _, c := range opts {
		if c.ctx > maxCtx {
			maxCtx = c.ctx
		}
	}
	target := BalancedCtx
	if maxCtx < target {
		target = maxCtx
	}
	good := make([]candidate, 0, len(opts))
	for _, c := range opts {
		if c.ctx >= target {
			good = append(good, c)
		}
	}
	if len(good) == 0 {
		good = opts
	}
	bestQ := qrank[good[0].quant]
	for _, c := range good {
		if qrank[c.quant] > bestQ {
			bestQ = qrank[c.quant]
		}
	}
	pool := make([]candidate, 0, len(good))
	for _, c := range good {
		if qrank[c.quant] == bestQ {
			pool = append(pool, c)
		}
	}
	return best(pool, func(c candidate) [3]int { return [3]int{c.ctx, 0, 0} }), true
}

func greater(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// CtxSlug is a short label for a context length, exact and lossless.
//
// Models are advertised in either decimal or binary thousands - Gemma's 128000
// and Qwen's 131072 are both called "128K" in the wild - so try decimal first
// and fall back to binary. ByTier checks the result is still unique per model,
// since a clash would make llama.cpp refuse to start with
// "model 'x' appears multiple times".
func CtxSlug(n int) string {
	for _, d := range []struct {
		div    int
		suffix string
	}{{1_000_000, "m"}, {1_000, "k"}} {
		if n%d.div == 0 {
			return strconv.Itoa(n/d.div) + d.suffix
		}
	}
	if n%1024 == 0 {
		k := n / 1024
		if k%1024 == 0 {
			return strconv.Itoa(k/1024) + "m"
		}
		return strconv.Itoa(k) + "k"
	}
	return strconv.Itoa(n)
}

func FmtCtx(n int) string { return strings.ToUpper(CtxSlug(n)) }

// SectionName is the section name, which is also the directory name the user
// must create under --models-dir.
func SectionName(id, quant string, ctx int, kv string) string {
	return fmt.Sprintf("%s-%s-%s-%s", id, strings.ToLower(quant), CtxSlug(ctx), kv)
}

// Tier is one output file: every setup that fits a given VRAM budget.
type Tier struct {
	GB      int
	Entries []*Setup
}

func (t *Tier) ModelCount() int {
	seen := map[string]bool{}
	for _, e := range t.Entries {
		seen[e.ID] = true
	}
	return len(seen)
}

// Plan chooses a setup per (tier, profile, model), then merges the profiles
// that landed on the same triple into a single section.
//
// That merge is the reason a tier fits in one file: a model at its maximum
// context is picked identically by every profile, so it appears once.
func Plan(est *Estimates, models []*ModelConfig) ([]*Tier, error) {
	var out []*Tier

	for _, tierGB := range Tiers {
		budget := float64(tierGB) - HeadroomGiB

		var order []string // merge keys, in insertion order
		merged := map[string]*Setup{}

		for _, profile := range Profiles {
			for _, cfg := range models {
				d, ok := est.Data[cfg.ID]
				if !ok {
					continue
				}
				got, ok := pick(d, budget, profile, cfg.Cap)
				if !ok {
					continue
				}
				key := fmt.Sprintf("%s\x00%s\x00%d\x00%s", cfg.ID, got.quant, got.ctx, got.kv)
				if e, seen := merged[key]; seen {
					e.Profiles = append(e.Profiles, profile.Name)
					continue
				}
				merged[key] = &Setup{
					ModelConfig: cfg,
					KV:          got.kv,
					Quant:       got.quant,
					Ctx:         got.ctx,
					GiB:         got.gib,
					Profiles:    []string{profile.Name},
					Name:        SectionName(cfg.ID, got.quant, got.ctx, got.kv),
				}
				order = append(order, key)
			}
		}
		if len(merged) == 0 {
			continue
		}

		entries := make([]*Setup, 0, len(order))
		for _, k := range order {
			entries = append(entries, merged[k])
		}
		sort.SliceStable(entries, func(i, j int) bool {
			if entries[i].ID != entries[j].ID {
				return entries[i].ID < entries[j].ID
			}
			return profileIndex(entries[i].Profiles[0]) < profileIndex(entries[j].Profiles[0])
		})

		if err := checkUniqueNames(tierGB, entries); err != nil {
			return nil, err
		}
		out = append(out, &Tier{GB: tierGB, Entries: entries})
	}
	return out, nil
}

func checkUniqueNames(tierGB int, entries []*Setup) error {
	count := map[string]int{}
	for _, e := range entries {
		count[e.Name]++
	}
	var clashes []string
	for name, n := range count {
		if n > 1 {
			clashes = append(clashes, name)
		}
	}
	if len(clashes) == 0 {
		return nil
	}
	sort.Strings(clashes)
	return fmt.Errorf("tier %d: two different setups share a section name: %s\nfix CtxSlug() in src/internal/preset/plan.go",
		tierGB, strings.Join(clashes, ", "))
}
