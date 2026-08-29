package preset

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// configs/vram-estimates.json is committed, so reading and rewriting it must be
// byte-stable or every measurement run produces a 76 KB diff. Two things in it
// defeat encoding/json:
//
//   - Key order carries meaning. measure.py sorted "quants" by value ascending,
//     and build's candidate loop iterates that order, which decides ties. Go maps
//     randomise iteration, and json.Marshal sorts keys alphabetically.
//   - Whole numbers are written "5.0", not "5". Python's json emits float 5.0 as
//     "5.0"; Go's emits "5". Eight values in the file are affected.
//
// So this is a minimal ordered JSON model that keeps insertion order and stores
// numbers as their original literal text. Round-tripping is byte-identical by
// construction, and the output matches Python's json.dumps(indent=2).

type jsonValue interface {
	writeTo(w *strings.Builder, depth int)
}

type jsonObject struct {
	keys []string
	vals map[string]jsonValue
}

type jsonArray []jsonValue

// jsonNumber is the literal text as it appeared in the file, never re-formatted.
type jsonNumber string

type jsonString string

type jsonBool bool

type jsonNull struct{}

func newObject() *jsonObject {
	return &jsonObject{vals: map[string]jsonValue{}}
}

func (o *jsonObject) get(k string) (jsonValue, bool) {
	v, ok := o.vals[k]
	return v, ok
}

// set appends k on first use and overwrites in place afterwards, so rewriting an
// existing key keeps its original position.
func (o *jsonObject) set(k string, v jsonValue) {
	if _, seen := o.vals[k]; !seen {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

// reorder rewrites the key order, keeping only keys already present.
func (o *jsonObject) reorder(keys []string) {
	next := make([]string, 0, len(keys))
	for _, k := range keys {
		if _, ok := o.vals[k]; ok {
			next = append(next, k)
		}
	}
	o.keys = next
}

func (o *jsonObject) float(k string) (float64, bool) {
	v, ok := o.vals[k]
	if !ok {
		return 0, false
	}
	n, ok := v.(jsonNumber)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(n), 64)
	return f, err == nil
}

func (o *jsonObject) object(k string) (*jsonObject, bool) {
	v, ok := o.vals[k]
	if !ok {
		return nil, false
	}
	obj, ok := v.(*jsonObject)
	return obj, ok
}

// parseJSON decodes into the ordered model above, preserving key order and
// number literals.
func parseJSON(data []byte) (jsonValue, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // keeps the literal text instead of converting to float64
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after top-level JSON value")
	}
	return v, nil
}

func parseValue(dec *json.Decoder) (jsonValue, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return parseFromToken(dec, tok)
}

func parseFromToken(dec *json.Decoder, tok json.Token) (jsonValue, error) {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := newObject()
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string: %v", keyTok)
				}
				val, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				obj.set(key, val)
			}
			if _, err := dec.Token(); err != nil { // consume '}'
				return nil, err
			}
			return obj, nil
		case '[':
			arr := jsonArray{}
			for dec.More() {
				val, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			if _, err := dec.Token(); err != nil { // consume ']'
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected delimiter %v", t)
	case json.Number:
		return jsonNumber(t.String()), nil
	case string:
		return jsonString(t), nil
	case bool:
		return jsonBool(t), nil
	case nil:
		return jsonNull{}, nil
	}
	return nil, fmt.Errorf("unsupported JSON token %T", tok)
}

// formatJSON renders with two-space indentation, matching json.dumps(indent=2).
func formatJSON(v jsonValue) string {
	var w strings.Builder
	v.writeTo(&w, 0)
	return w.String()
}

func indent(w *strings.Builder, depth int) {
	for i := 0; i < depth; i++ {
		w.WriteString("  ")
	}
}

func (o *jsonObject) writeTo(w *strings.Builder, depth int) {
	if len(o.keys) == 0 {
		w.WriteString("{}")
		return
	}
	w.WriteString("{\n")
	for i, k := range o.keys {
		indent(w, depth+1)
		writeJSONString(w, k)
		w.WriteString(": ")
		o.vals[k].writeTo(w, depth+1)
		if i < len(o.keys)-1 {
			w.WriteByte(',')
		}
		w.WriteByte('\n')
	}
	indent(w, depth)
	w.WriteByte('}')
}

func (a jsonArray) writeTo(w *strings.Builder, depth int) {
	if len(a) == 0 {
		w.WriteString("[]")
		return
	}
	w.WriteString("[\n")
	for i, v := range a {
		indent(w, depth+1)
		v.writeTo(w, depth+1)
		if i < len(a)-1 {
			w.WriteByte(',')
		}
		w.WriteByte('\n')
	}
	indent(w, depth)
	w.WriteByte(']')
}

func (n jsonNumber) writeTo(w *strings.Builder, _ int) { w.WriteString(string(n)) }

func (s jsonString) writeTo(w *strings.Builder, _ int) { writeJSONString(w, string(s)) }

func (b jsonBool) writeTo(w *strings.Builder, _ int) {
	if b {
		w.WriteString("true")
	} else {
		w.WriteString("false")
	}
}

func (jsonNull) writeTo(w *strings.Builder, _ int) { w.WriteString("null") }

// writeJSONString escapes the way Python's json does with ensure_ascii=True:
// non-ASCII becomes \uXXXX, and '<', '>', '&' are left alone (Go's encoder
// escapes those by default, which would corrupt the file).
func writeJSONString(w *strings.Builder, s string) {
	w.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			w.WriteString(`\"`)
		case '\\':
			w.WriteString(`\\`)
		case '\n':
			w.WriteString(`\n`)
		case '\r':
			w.WriteString(`\r`)
		case '\t':
			w.WriteString(`\t`)
		case '\b':
			w.WriteString(`\b`)
		case '\f':
			w.WriteString(`\f`)
		default:
			switch {
			case r < 0x20:
				fmt.Fprintf(w, `\u%04x`, r)
			case r < 0x7f:
				w.WriteRune(r)
			case r > 0xffff: // surrogate pair, as Python emits
				r -= 0x10000
				fmt.Fprintf(w, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
			default:
				fmt.Fprintf(w, `\u%04x`, r)
			}
		}
	}
	w.WriteByte('"')
}

// formatGiB renders a measured value the way Python's round(x, 2) then repr()
// does: two decimal places, trailing zeros trimmed, but never bare ("5" -> "5.0").
func formatGiB(f float64) jsonNumber {
	s := strconv.FormatFloat(f, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	if strings.HasSuffix(s, ".") {
		s += "0"
	}
	return jsonNumber(s)
}
