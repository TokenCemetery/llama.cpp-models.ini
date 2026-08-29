package preset

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Root finds the repository root by walking up from the working directory
// looking for configs/vram-estimates.json.
//
// It deliberately does not look for go.mod: that lives in src/, so finding it
// would stop one directory short of the root and every path would be wrong.
// The marker has to be something only the real root has.
func Root() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "configs", "vram-estimates.json")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not inside the repository: no configs/vram-estimates.json found above %s", dir)
		}
		dir = parent
	}
}

// Load reads both inputs and refuses to continue on incomplete measurements,
// since a missing curve would silently drop a model from every tier.
func Load(root string) (*Estimates, []*ModelConfig, []*Tier, error) {
	est, err := LoadEstimates(root)
	if err != nil {
		return nil, nil, nil, err
	}
	models, err := LoadModelConfigs(root)
	if err != nil {
		return nil, nil, nil, err
	}

	needed := NeededKV()
	var incomplete []string
	for _, cfg := range models {
		d, ok := est.Data[cfg.ID]
		if !ok || len(d.Quants) == 0 {
			incomplete = append(incomplete, cfg.ID)
			continue
		}
		for kv := range needed {
			if _, ok := d.Curves[kv]; !ok {
				incomplete = append(incomplete, cfg.ID)
				break
			}
		}
	}
	if len(incomplete) > 0 {
		return nil, nil, nil, fmt.Errorf("missing measurements for: %s\nrun: go -C src run ./cmd/llamapreset measure --missing",
			strings.Join(incomplete, ", "))
	}

	tiers, err := Plan(est, models)
	if err != nil {
		return nil, nil, nil, err
	}
	return est, models, tiers, nil
}

// Build writes dist/ and MODELS.md.
func Build(root string) error {
	_, models, tiers, err := Load(root)
	if err != nil {
		return err
	}
	if err := WriteDist(root, tiers); err != nil {
		return err
	}
	for _, t := range tiers {
		fmt.Printf("wrote %s  (%d models, %d setups)\n",
			rel(root, DistPath(root, t.GB)), t.ModelCount(), len(t.Entries))
	}

	// Committed, unlike dist/, so it can be browsed on GitHub. CI checks it is fresh.
	catalogue := filepath.Join(root, "MODELS.md")
	if err := os.WriteFile(catalogue, []byte(RenderModelsMD(tiers, models)), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s  (%d models)\n", rel(root, catalogue), len(models))
	return nil
}

// Notes prints the release body to stdout.
func Notes(root string) error {
	est, _, tiers, err := Load(root)
	if err != nil {
		return err
	}
	fmt.Println(RenderNotes(tiers, est))
	return nil
}
