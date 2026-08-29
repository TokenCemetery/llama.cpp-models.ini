package preset

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// Estimates is configs/vram-estimates.json. It keeps the parsed ordered tree
// around so a rewrite preserves everything this program does not touch,
// including the whole _meta block.
type Estimates struct {
	root   *jsonObject
	models *jsonObject

	IDs  []string // model ids in file order
	Data map[string]*ModelData
}

// ModelData is one model's measurements.
//
// QuantKeys is the file's own order (measure writes it sorted by VRAM
// ascending) and the candidate loop walks it in that order. Since a pick
// resolves ties by taking the first maximal option, that order is load-bearing:
// re-sorting it would silently change which quant some models get.
type ModelData struct {
	obj *jsonObject

	MaxCtx    int
	RefQuant  string
	QuantKeys []string
	Quants    map[string]float64
	KVKeys    []string // KV cache types in file order
	Curves    map[string]*Curve
}

// Curve is VRAM of the reference quant across context sizes, for one KV type.
type Curve struct {
	obj    *jsonObject
	Keys   []string // context sizes as they appear, numeric strings
	Vals   map[string]float64
	Ladder []int // Keys parsed and sorted ascending
}

func estimatesPath(root string) string {
	return filepath.Join(root, "configs", "vram-estimates.json")
}

func LoadEstimates(root string) (*Estimates, error) {
	raw, err := os.ReadFile(estimatesPath(root))
	if err != nil {
		return nil, err
	}
	v, err := parseJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("configs/vram-estimates.json: %w", err)
	}
	obj, ok := v.(*jsonObject)
	if !ok {
		return nil, errors.New("configs/vram-estimates.json: top level is not an object")
	}
	models, ok := obj.object("models")
	if !ok {
		return nil, errors.New(`configs/vram-estimates.json: no "models" object`)
	}

	est := &Estimates{root: obj, models: models, Data: map[string]*ModelData{}}
	for _, id := range models.keys {
		mo, ok := models.object(id)
		if !ok {
			return nil, fmt.Errorf("configs/vram-estimates.json: %q is not an object", id)
		}
		est.IDs = append(est.IDs, id)
		est.Data[id] = newModelData(mo)
	}
	return est, nil
}

func newModelData(obj *jsonObject) *ModelData {
	d := &ModelData{obj: obj, Quants: map[string]float64{}, Curves: map[string]*Curve{}}

	if f, ok := obj.float("max_ctx"); ok {
		d.MaxCtx = int(f)
	}
	if v, ok := obj.get("ref_quant"); ok {
		if s, ok := v.(jsonString); ok {
			d.RefQuant = string(s)
		}
	}
	if quants, ok := obj.object("quants"); ok {
		for _, q := range quants.keys {
			if f, ok := quants.float(q); ok {
				d.QuantKeys = append(d.QuantKeys, q)
				d.Quants[q] = f
			}
		}
	}
	if curves, ok := obj.object("ref_curves"); ok {
		for _, kv := range curves.keys {
			co, ok := curves.object(kv)
			if !ok {
				continue
			}
			c := &Curve{obj: co, Vals: map[string]float64{}}
			for _, ctx := range co.keys {
				f, ok := co.float(ctx)
				if !ok {
					continue
				}
				n, err := strconv.Atoi(ctx)
				if err != nil {
					continue
				}
				c.Keys = append(c.Keys, ctx)
				c.Vals[ctx] = f
				c.Ladder = append(c.Ladder, n)
			}
			sort.Ints(c.Ladder)
			d.KVKeys = append(d.KVKeys, kv)
			d.Curves[kv] = c
		}
	}
	return d
}

// VRAM of one (quant, context, KV type) triple.
//
// KV cost per token does not depend on the weight quant, so each quant's curve
// is the reference curve shifted by a constant. The arithmetic is written in
// the same order as the Python it replaces so the float64 result is bit-identical.
func (d *ModelData) VRAM(quant string, ctx int, kv string) (float64, bool) {
	curve, ok := d.Curves[kv]
	if !ok {
		return 0, false
	}
	base, ok := curve.Vals[strconv.Itoa(ctx)]
	if !ok {
		return 0, false
	}
	offset := d.Quants[quant] - d.Quants[d.RefQuant]
	return base + offset, true
}

// Meta returns the _meta block's fields that rendering needs.
func (e *Estimates) MetaString(key string) string {
	meta, ok := e.root.object("_meta")
	if !ok {
		return ""
	}
	v, ok := meta.get(key)
	if !ok {
		return ""
	}
	s, ok := v.(jsonString)
	if !ok {
		return ""
	}
	return string(s)
}

// Entry returns the model's object, creating it (with the same key order
// measure.py used) when absent.
func (e *Estimates) Entry(id string) *ModelData {
	if d, ok := e.Data[id]; ok {
		return d
	}
	mo := newObject()
	mo.set("max_ctx", jsonNull{})
	mo.set("ref_quant", jsonNull{})
	mo.set("ref_curves", newObject())
	mo.set("quants", newObject())
	e.models.set(id, mo)
	d := newModelData(mo)
	e.IDs = append(e.IDs, id)
	e.Data[id] = d
	return d
}

// SetQuant records a quant measurement, keeping "quants" sorted by VRAM
// ascending as measure.py did.
func (e *Estimates) SetQuant(id, quant string, gib float64) {
	d := e.Entry(id)
	quants, ok := d.obj.object("quants")
	if !ok {
		quants = newObject()
		d.obj.set("quants", quants)
	}
	quants.set(quant, formatGiB(gib))

	keys := append([]string(nil), quants.keys...)
	sort.SliceStable(keys, func(i, j int) bool {
		a, _ := quants.float(keys[i])
		b, _ := quants.float(keys[j])
		return a < b
	})
	quants.reorder(keys)

	d.Quants[quant] = gib
	d.QuantKeys = append([]string(nil), quants.keys...)
}

// SetCurvePoint records one context measurement, keeping the curve in
// ascending context order.
func (e *Estimates) SetCurvePoint(id, kv string, ctx int, gib float64) {
	d := e.Entry(id)
	curves, ok := d.obj.object("ref_curves")
	if !ok {
		curves = newObject()
		d.obj.set("ref_curves", curves)
	}
	co, ok := curves.object(kv)
	if !ok {
		co = newObject()
		curves.set(kv, co)
	}
	co.set(strconv.Itoa(ctx), formatGiB(gib))

	keys := append([]string(nil), co.keys...)
	sort.SliceStable(keys, func(i, j int) bool {
		a, _ := strconv.Atoi(keys[i])
		b, _ := strconv.Atoi(keys[j])
		return a < b
	})
	co.reorder(keys)

	// Refresh the typed view.
	*d = *newModelData(d.obj)
}

func (e *Estimates) SetMaxCtx(id, refQuant string, maxCtx int) {
	d := e.Entry(id)
	d.obj.set("max_ctx", jsonNumber(strconv.Itoa(maxCtx)))
	d.obj.set("ref_quant", jsonString(refQuant))
	d.MaxCtx = maxCtx
	d.RefQuant = refQuant
}

// Save rewrites configs/vram-estimates.json. Untouched values keep their exact
// original text, so a run that changes nothing produces no diff.
func (e *Estimates) Save(root string) error {
	return os.WriteFile(estimatesPath(root), []byte(formatJSON(e.root)+"\n"), 0o644)
}
