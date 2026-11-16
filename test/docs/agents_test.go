package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The agents' guidance is one file, AGENTS.md. CLAUDE.md only imports it:
// rules written into a tool's own file would hold for that tool alone.
func TestOneGuidanceFile(t *testing.T) {
	root := filepath.Join("..", "..")
	b, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	var imports int
	for _, l := range strings.Split(string(b), "\n") {
		switch {
		case l == "@AGENTS.md":
			imports++
		case strings.HasPrefix(l, "- "), strings.HasPrefix(l, "## "):
			t.Errorf("CLAUDE.md has guidance of its own (%q): it belongs in AGENTS.md", l)
		}
	}
	if imports != 1 {
		t.Errorf("CLAUDE.md imports AGENTS.md %d times, want once (a line @AGENTS.md)", imports)
	}
}

// What AGENTS.md points to is there: a file or directory it names in
// backticks, by a path from the repository's root.
func TestGuidancePaths(t *testing.T) {
	root := filepath.Join("..", "..")
	b, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(strings.Fields(string(b)), " ") // a path may be wrapped
	n := 0
	for _, m := range regexp.MustCompile("`([A-Za-z0-9_./-]+)`").FindAllStringSubmatch(text, -1) {
		p := strings.TrimSuffix(m[1], "/")
		if !strings.ContainsAny(p, "/.") || strings.HasPrefix(p, "/") || strings.Contains(p, "*") {
			continue // a word, an installed path, a pattern
		}
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			if strings.Contains(p, "/") || strings.HasSuffix(p, ".md") {
				t.Errorf("AGENTS.md names %s, which is not in the repository", p)
			}
			continue
		}
		n++
	}
	if n < 10 {
		t.Errorf("only %d paths of AGENTS.md were checked: the test no longer reads it", n)
	}
}
