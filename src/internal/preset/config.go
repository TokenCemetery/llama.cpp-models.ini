package preset

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ModelConfig is one file under configs/: tuning settings for a single model,
// plus the comment metadata MODELS.md and the measurement step read.
type ModelConfig struct {
	Path     string   // repo-relative, for error messages
	Provider string   // parent directory name
	ID       string   // section name, e.g. "qwen3-8b"
	Title    string   // first comment line
	Repo     string   // "; repo:" - the HuggingFace GGUF repo
	Params   string   // "; params:" - e.g. "8B" or "30B-A3B"
	Tags     []string // "; tags:" - e.g. vision, reasoning
	Doc      []string // every leading comment line, copied into dist/ verbatim
	Body     []string // the [section] line and its keys, minus any ctx-size cap
	Cap      int      // ctx-size upper limit from the config, 0 when unset
}

var sectionRe = regexp.MustCompile(`(?m)^\[([^\]]+)\]`)

// LoadModelConfigs reads every configs/**/*.ini, sorted by full path.
//
// The sort is on the whole path string rather than per-directory, matching
// Python's sorted(Path.rglob(...)). The two orders differ once a provider
// directory name is a prefix of another, and this order reaches dist/.
func LoadModelConfigs(root string) ([]*ModelConfig, error) {
	configDir := filepath.Join(root, "configs")

	var paths []string
	err := filepath.WalkDir(configDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".ini") {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	out := make([]*ModelConfig, 0, len(paths))
	for _, p := range paths {
		cfg, err := parseModelConfig(root, p)
		if err != nil {
			return nil, err
		}
		out = append(out, cfg)
	}
	return out, nil
}

func parseModelConfig(root, path string) (*ModelConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := string(raw)

	loc := sectionRe.FindStringSubmatchIndex(text)
	if loc == nil {
		return nil, fmt.Errorf("%s: no [section] found", rel(root, path))
	}
	id := strings.TrimSpace(text[loc[2]:loc[3]])
	head := text[:loc[0]]

	var doc []string
	for _, line := range strings.Split(head, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), ";") {
			doc = append(doc, line)
		}
	}

	// The [section] line onward, with any ctx-size line lifted out as a cap.
	ctxCap := 0
	var body []string
	for _, line := range strings.Split(strings.TrimRight(text[loc[0]:], " \t\n\r\v\f"), "\n") {
		if key, value, found := strings.Cut(line, "="); found {
			if strings.TrimSpace(key) == "ctx-size" {
				n, err := strconv.Atoi(strings.TrimSpace(value))
				if err != nil {
					return nil, fmt.Errorf("%s: ctx-size is not a number: %q", rel(root, path), value)
				}
				ctxCap = n
				continue
			}
		}
		body = append(body, line)
	}

	title := id
	if len(doc) > 0 {
		title = strings.TrimSpace(strings.TrimLeft(doc[0], "; "))
	}

	cfg := &ModelConfig{
		Path:     rel(root, path),
		Provider: filepath.Base(filepath.Dir(path)),
		ID:       id,
		Title:    title,
		Repo:     headField(head, "repo"),
		Params:   headField(head, "params"),
		Doc:      doc,
		Body:     body,
		Cap:      ctxCap,
	}
	for _, t := range strings.Split(headField(head, "tags"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			cfg.Tags = append(cfg.Tags, t)
		}
	}
	return cfg, nil
}

func headField(head, name string) string {
	re := regexp.MustCompile(`(?m)^;\s*` + regexp.QuoteMeta(name) + `:\s*(.+)$`)
	if m := re.FindStringSubmatch(head); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func rel(root, path string) string {
	if r, err := filepath.Rel(root, path); err == nil {
		return filepath.ToSlash(r)
	}
	return path
}

// IniEntry is one line of interest from an INI file. Key is empty on a section
// header line, which is what validate uses to tell the two apart.
type IniEntry struct {
	Line    int
	Section string
	Key     string
	Value   string
}

// ParseIni yields section headers and key/value pairs, skipping blanks and
// comments. It deliberately mirrors llama.cpp's own lenient parsing rather than
// using a full INI library: values are taken raw, so validate can catch the
// ';' truncation trap instead of a library silently handling it.
func ParseIni(text string) []IniEntry {
	var out []IniEntry
	section := ""
	for n, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(line, "[]")
			out = append(out, IniEntry{Line: n + 1, Section: section})
			continue
		}
		if key, value, found := strings.Cut(line, "="); found {
			out = append(out, IniEntry{
				Line:    n + 1,
				Section: section,
				Key:     strings.TrimSpace(key),
				Value:   strings.TrimSpace(value),
			})
		}
	}
	return out
}
