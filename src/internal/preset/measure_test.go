package preset

import "testing"

// An out-of-band tag must never be read as the in-band tag it contains.
// "UD-Q6_K.gguf" ends with "Q6_K.gguf", so a band-only alternation matched it
// and recorded those files as plain Q6_K - a number measured from the wrong
// file, in repos that ship no plain Q6_K at all.
func TestQuantOf(t *testing.T) {
	cases := []struct{ file, want string }{
		// In band.
		{"gemma-3-27b-it-UD-Q6_K_XL.gguf", "UD-Q6_K_XL"},
		{"gemma-3-27b-it-Q6_K.gguf", "Q6_K"},
		{"gemma-3-27b-it-UD-Q3_K_XL.gguf", "UD-Q3_K_XL"},
		{"gpt-oss-20b-Q5_K_M.gguf", "Q5_K_M"},
		{"gemma-4-31B-it-qat-UD-Q4_K_XL.gguf", "UD-Q4_K_XL"},

		// Old Unsloth names that contain an in-band tag.
		{"Qwen3.8-27B-UD-Q6_K.gguf", ""},
		{"Qwen3.8-27B-UD-Q5_K_M.gguf", ""},
		{"Qwen3.8-27B-UD-Q4_K_M.gguf", ""},

		// Out of band on both sides of the band.
		{"Qwen3.8-27B-UD-Q8_K_XL.gguf", ""},
		{"Qwen3.8-27B-Q8_0.gguf", ""},
		{"Qwen3.8-27B-UD-Q6_K_L.gguf", ""},
		{"Qwen3.8-27B-UD-Q2_K_XL.gguf", ""},
		{"Qwen3.8-27B-Q4_K_S.gguf", ""},
		{"Qwen3.8-27B-UD-IQ3_XXS.gguf", ""},
		{"Qwen3.8-27B-BF16.gguf", ""},
		{"mmproj-F16.gguf", ""},
	}
	for _, c := range cases {
		if got := quantOf(c.file); got != c.want {
			t.Errorf("quantOf(%q) = %q, want %q", c.file, got, c.want)
		}
	}
}

// Every rung must be reachable through quantOf, or measure would look for a
// quant it can never recognise.
func TestQuantLadderIsRecognisable(t *testing.T) {
	for _, q := range QuantLadder {
		file := "some-model-" + q + ".gguf"
		if got := quantOf(file); got != q {
			t.Errorf("quantOf(%q) = %q, want %q", file, got, q)
		}
		if quantRank[q] == 0 {
			t.Errorf("%q has no rank", q)
		}
	}
}
