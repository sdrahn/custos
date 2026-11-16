// Package tools checks tools/changelog (changelog_test.go), and
// tools/check-release and tools/check-tree, which
// the Release workflow runs before it publishes a tag, against throwaway
// git repositories.
package tools

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const releaseService = `<services>
  <service name="tar_scm" mode="manual">
    <param name="revision">release-0.1</param>
    <param name="versionformat">@PARENT_TAG@</param>
  </service>
</services>
`

const mainService = `<services>
  <service name="tar_scm" mode="manual">
    <param name="revision">main</param>
    <param name="versionformat">0.1.0+git%cd.%h</param>
  </service>
</services>
`

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// repo makes a repository with the tools and these files, committed.
func repo(t *testing.T, files map[string][]byte) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	d := t.TempDir()
	for _, tool := range []string{"check-release", "check-tree", "changelog"} {
		b, err := os.ReadFile(filepath.Join("../../tools", tool))
		if err != nil {
			t.Fatal(err)
		}
		files["tools/"+tool] = b
	}
	for name, b := range files {
		p := filepath.Join(d, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git(t, d, "init", "-q")
	git(t, d, "add", "-A")
	git(t, d, "commit", "-q", "-m", "x")
	return d
}

func release(version, changelog, service string) map[string][]byte {
	return map[string][]byte{
		"packaging/suse/custos.spec": []byte("Name: custos\nVersion:        " + version + "\n"),
		"packaging/suse/_service":    []byte(service),
		"CHANGELOG.md":               []byte("# Changelog\n\n" + changelog + "\n\n### Added\n\n- x\n"),
	}
}

func run(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestReleaseBranchIsReady(t *testing.T) {
	d := repo(t, release("0.1.0", "## v0.1.0 — 2026-11-04", releaseService))
	if out, err := run(t, d, "tools/check-release", "v0.1.0"); err != nil || !strings.Contains(out, "is ready for v0.1.0") {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestRefusesWhatDoesNotNameTheRelease(t *testing.T) {
	for _, c := range []struct {
		name  string
		files map[string][]byte
		says  string
	}{
		{"main's _service", release("0.1.0", "## v0.1.0 — 2026-11-04", mainService), "does not build from release-0.1"},
		{"main's _service version", release("0.1.0", "## v0.1.0 — 2026-11-04", mainService), "versionformat @PARENT_TAG@"},
		{"the spec", release("0.1.1", "## v0.1.0 — 2026-11-04", releaseService), "Version: 0.1.1, not 0.1.0"},
		{"Unreleased", release("0.1.0", "## Unreleased", releaseService), `is "## Unreleased", not`},
		{"another tag", release("0.1.0", "## v0.0.9 — 2026-11-01", releaseService), "not \"## v0.1.0"},
	} {
		d := repo(t, c.files)
		out, err := run(t, d, "tools/check-release", "v0.1.0")
		if err == nil || !strings.Contains(out, c.says) {
			t.Errorf("%s: %v, %q, want %q", c.name, err, out, c.says)
		}
	}
}

func TestCandidateNamesItsBase(t *testing.T) {
	d := repo(t, release("0.2.0", "## v0.2.0-rc1 — 2026-11-04", strings.ReplaceAll(releaseService, "0.1", "0.2")))
	if out, err := run(t, d, "tools/check-release", "v0.2.0-rc1"); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestNotATag(t *testing.T) {
	d := repo(t, release("0.1.0", "## v0.1.0 — 2026-11-04", releaseService))
	if out, err := run(t, d, "tools/check-release", "0.1"); err == nil || !strings.Contains(out, "is not a tag vX.Y.Z") {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestTree(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n\x00\x00"), bytes.Repeat([]byte{1}, 100)...)
	elf := []byte("\x7fELF\x02\x01\x01\x00\x00\x00")
	for _, c := range []struct {
		name  string
		files map[string][]byte
		says  string // "" for clean
	}{
		{"sources", map[string][]byte{"main.go": []byte("package main\n")}, ""},
		{"screenshots under docs/", map[string][]byte{"docs/screenshots/a.png": png}, ""},
		{"a program", map[string][]byte{"agent-admin": elf}, "agent-admin is a binary file"},
		{"an image outside docs/", map[string][]byte{"cockpit/custos/a.png": png}, "cockpit/custos/a.png is a binary file"},
		{"over 1 MiB", map[string][]byte{"big.txt": bytes.Repeat([]byte("a\n"), 600000)}, "big.txt is 1200000 bytes"},
	} {
		d := repo(t, c.files)
		out, err := run(t, d, "tools/check-tree")
		if c.says == "" && err != nil {
			t.Errorf("%s: %v: %s", c.name, err, out)
		}
		if c.says != "" && (err == nil || !strings.Contains(out, c.says)) {
			t.Errorf("%s: %v, %q, want %q", c.name, err, out, c.says)
		}
	}
}

// This repository's own tree, where it is a git checkout (not in the
// tarball the spec's %check runs in).
func TestThisTree(t *testing.T) {
	if err := exec.Command("git", "-C", "../..", "rev-parse", "--git-dir").Run(); err != nil {
		t.Skip("not a git checkout")
	}
	cmd := exec.Command("sh", "tools/check-tree")
	cmd.Dir = "../.."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

// The release notes are the tag's section of CHANGELOG.md, as the Release
// workflow extracts it. The package's tarball has no .github (_service
// excludes it), so the spec's %check has no workflow to read.
func TestReleaseNotes(t *testing.T) {
	b, err := os.ReadFile("../../.github/workflows/release.yml")
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("no .github/workflows/release.yml (a source tarball)")
	}
	if err != nil {
		t.Fatal(err)
	}
	wf := string(b)
	i := strings.Index(wf, "awk -v tag=")
	j := strings.Index(wf[i:], "CHANGELOG.md > notes.md")
	if i < 0 || j < 0 {
		t.Fatal("no notes extraction in release.yml")
	}
	script := strings.TrimSpace(wf[i:i+j]) + " CHANGELOG.md"
	d := t.TempDir()
	cl := "# Changelog\n\n## v0.2.0 — 2026-12-01\n\nTwo.\n\n## v0.1.0 — 2026-11-04\n\nOne.\n\n### Added\n\n- a\n"
	if err := os.WriteFile(filepath.Join(d, "CHANGELOG.md"), []byte(cl), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = d
	cmd.Env = append(os.Environ(), "GITHUB_REF_NAME=v0.1.0")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "One.\n\n### Added\n\n- a" {
		t.Fatalf("notes %q", got)
	}
}
