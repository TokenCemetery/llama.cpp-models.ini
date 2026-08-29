package preset

import (
	"os"
	"path/filepath"
	"testing"
)

// The estimates file is committed, so read-then-write must not change a byte.
func TestEstimatesRoundTripsByteIdentical(t *testing.T) {
	root, err := Root() // walks up past src/, so the test survives being moved
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	want, err := os.ReadFile(filepath.Join(root, "configs", "vram-estimates.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	v, err := parseJSON(want)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := formatJSON(v) + "\n" // measure.py appends the newline json.dumps omits
	if got != string(want) {
		for i := 0; i < len(got) && i < len(want); i++ {
			if got[i] != want[i] {
				lo := max(0, i-60)
				t.Fatalf("diverges at byte %d\n want: %q\n  got: %q", i,
					string(want[lo:min(len(want), i+60)]), got[lo:min(len(got), i+60)])
			}
		}
		t.Fatalf("length differs: got %d want %d", len(got), len(want))
	}
}

func TestFormatGiB(t *testing.T) {
	for _, c := range []struct {
		in   float64
		want jsonNumber
	}{
		{7.75, "7.75"},
		{5.0, "5.0"},   // Python repr(5.0) == "5.0", not "5"
		{4.10, "4.1"},  // trailing zero trimmed
		{18.0, "18.0"}, // real value from the file
		{20.2, "20.2"},
	} {
		if got := formatGiB(c.in); got != c.want {
			t.Errorf("formatGiB(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
