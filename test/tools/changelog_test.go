package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const released = "## v0.1.0 — 2026-11-04\n\nThe first.\n\n### Added\n\n- x\n"

func changelogRepo(t *testing.T, fragments map[string]string) string {
	t.Helper()
	files := map[string][]byte{
		"CHANGELOG.md":        []byte("# Changelog\n\nAll notable changes.\n\n" + released),
		"changes.d/README.md": []byte("# changes.d\n\n## A fragment\n\nnot a fragment itself\n"),
	}
	for name, s := range fragments {
		files["changes.d/"+name] = []byte(s)
	}
	return repo(t, files)
}

// The repository's own changes.d holds fragments only: make check and CI
// run this.
func TestChangelogOfThisRepository(t *testing.T) {
	if out, err := run(t, ".", "../../tools/changelog", "check"); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestChangelogRelease(t *testing.T) {
	d := changelogRepo(t, map[string]string{
		"intro.md":              "What 0.2.0 is about.\n\nUpgrading needs nothing.\n",
		"zz-compare.fixed.md":   "- compare exits with 1.\n",
		"b-second.added.md":     "- A second thing,\n  on two lines.\n",
		"a-first.added.md":      "- A first thing.\n- And another of it.\n",
		"old-option.removed.md": "- `--old` is gone.\n",
	})
	show, err := run(t, d, "tools/changelog", "show")
	if err != nil {
		t.Fatalf("show: %v: %s", err, show)
	}
	want := "What 0.2.0 is about.\n\nUpgrading needs nothing.\n\n" +
		"### Added\n\n- A first thing.\n- And another of it.\n- A second thing,\n  on two lines.\n\n" +
		"### Fixed\n\n- compare exits with 1.\n\n" +
		"### Removed\n\n- `--old` is gone.\n\n"
	if show != want {
		t.Errorf("show:\n%q\nwant\n%q", show, want)
	}
	if out, err := run(t, d, "tools/changelog", "release", "v0.2.0", "2026-12-01"); err != nil {
		t.Fatalf("release: %v: %s", err, out)
	}
	b, _ := os.ReadFile(filepath.Join(d, "CHANGELOG.md"))
	if got, want := string(b), "# Changelog\n\nAll notable changes.\n\n## v0.2.0 — 2026-12-01\n\n"+want+released; got != want {
		t.Errorf("CHANGELOG.md:\n%q\nwant\n%q", got, want)
	}
	left, _ := filepath.Glob(filepath.Join(d, "changes.d", "*"))
	if len(left) != 1 || filepath.Base(left[0]) != "README.md" {
		t.Errorf("changes.d after the release: %v, want README.md alone", left)
	}
	// Nothing to release twice, and no second section of a tag.
	if out, err := run(t, d, "tools/changelog", "release", "v0.3.0", "2026-12-02"); err == nil || !strings.Contains(out, "no change to release") {
		t.Errorf("an empty release: %v: %s", err, out)
	}
	os.WriteFile(filepath.Join(d, "changes.d", "x.fixed.md"), []byte("- x\n"), 0o644)
	if out, err := run(t, d, "tools/changelog", "release", "v0.2.0"); err == nil || !strings.Contains(out, "already has a section for v0.2.0") {
		t.Errorf("the same tag again: %v: %s", err, out)
	}
	for _, args := range [][]string{{"release", "0.3.0"}, {"release", "v0.3.0", "tomorrow"}, {"release"}, {"frobnicate"}, {}} {
		if out, err := run(t, d, append([]string{"tools/changelog"}, args...)...); err == nil {
			t.Errorf("changelog %v: no error: %s", args, out)
		}
	}
	if b2, _ := os.ReadFile(filepath.Join(d, "CHANGELOG.md")); string(b2) != string(b) {
		t.Errorf("a refused release changed CHANGELOG.md")
	}
}

// A changelog without a released section yet gets its first one at the
// end.
func TestChangelogFirstRelease(t *testing.T) {
	d := repo(t, map[string][]byte{
		"CHANGELOG.md":           []byte("# Changelog\n\nAll notable changes.\n"),
		"changes.d/one.added.md": []byte("- One.\n"),
	})
	if out, err := run(t, d, "tools/changelog", "release", "v0.1.0", "2026-12-01"); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	b, _ := os.ReadFile(filepath.Join(d, "CHANGELOG.md"))
	if want := "# Changelog\n\nAll notable changes.\n\n## v0.1.0 — 2026-12-01\n\n### Added\n\n- One.\n\n"; string(b) != want {
		t.Errorf("%q, want %q", b, want)
	}
}

func TestChangelogCheckRefuses(t *testing.T) {
	for _, c := range []struct {
		name, content, says string
	}{
		{"Topic.fixed.md", "- x\n", "is not <topic>.<kind>.md"},
		{"topic.md", "- x\n", "is not <topic>.<kind>.md"},
		{"topic.improved.md", "- x\n", "is not <topic>.<kind>.md"},
		{"topic.fixed.txt", "- x\n", "is not <topic>.<kind>.md"},
		{"topic.fixed.md", "x is fixed\n", "does not start with a list item"},
		{"topic.fixed.md", "", "does not start with a list item"},
		{"topic.fixed.md", "- x", "does not end with a newline"},
		{"topic.fixed.md", "- x\nand more\n", "neither a list item"},
		{"topic.fixed.md", "- x\n\n### Fixed\n", "neither a list item"},
		{"topic.fixed.md", "- x\n\n", "ends with an empty line"},
		{"intro.md", "## v0.2.0\n\nAbout.\n", "intro.md has a heading"},
	} {
		d := changelogRepo(t, map[string]string{c.name: c.content, "fine.added.md": "- fine\n"})
		for _, cmd := range []string{"check", "show"} {
			if out, err := run(t, d, "tools/changelog", cmd); err == nil || !strings.Contains(out, c.says) {
				t.Errorf("%s %q, %s: %v, %q, want %q", c.name, c.content, cmd, err, out, c.says)
			}
		}
		before, _ := os.ReadFile(filepath.Join(d, "CHANGELOG.md"))
		if out, err := run(t, d, "tools/changelog", "release", "v0.2.0"); err == nil {
			t.Errorf("%s: released: %s", c.name, out)
		}
		if after, _ := os.ReadFile(filepath.Join(d, "CHANGELOG.md")); string(after) != string(before) {
			t.Errorf("%s: a refused release changed CHANGELOG.md", c.name)
		}
	}
	// What is fine: more items, continuation lines, an empty line between.
	d := changelogRepo(t, map[string]string{"a-b2.security.md": "- one\n  more\n\n- two\n", "intro.md": "About.\n"})
	if out, err := run(t, d, "tools/changelog", "check"); err != nil {
		t.Errorf("%v: %s", err, out)
	}
}

// A release with a fragment left is not ready: its change would be
// missing from the release's section.
func TestReleaseRefusesFragmentsLeft(t *testing.T) {
	files := release("0.1.0", "## v0.1.0 — 2026-11-04", releaseService)
	files["changes.d/README.md"] = []byte("# changes.d\n")
	d := repo(t, files)
	if out, err := run(t, d, "tools/check-release", "v0.1.0"); err != nil {
		t.Fatalf("with the README alone: %v: %s", err, out)
	}
	files["changes.d/late.fixed.md"] = []byte("- late\n")
	d = repo(t, files)
	out, err := run(t, d, "tools/check-release", "v0.1.0")
	if err == nil || !strings.Contains(out, "changes.d still has changes.d/late.fixed.md") || !strings.Contains(out, "tools/changelog release v0.1.0") {
		t.Errorf("%v: %s", err, out)
	}
}
