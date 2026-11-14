// Package vm holds the functional tests (run-tests.sh, lib.sh and the
// sections) and their helper programs; this test checks the scripts
// themselves where no test system is.
package vm

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func sections(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("sections/*")
	if err != nil || len(files) == 0 {
		t.Fatalf("sections: %v %v", files, err)
	}
	return files
}

func TestScriptParses(t *testing.T) {
	for _, f := range append([]string{"run-tests.sh", "lib.sh"}, sections(t)...) {
		if out, err := exec.Command("bash", "-n", f).CombinedOutput(); err != nil {
			t.Errorf("bash -n %s: %v\n%s", f, err, out)
		}
	}
}

// Every file in sections/ is one run-tests.sh runs (NN-topic.sh, the
// number its place in the order, used once) and has a section; the
// framework is where they expect it.
func TestSections(t *testing.T) {
	name := regexp.MustCompile(`^sections/([0-9]{2})-[a-z-]+\.sh$`)
	seen := map[string]string{}
	for _, f := range sections(t) {
		m := name.FindStringSubmatch(filepath.ToSlash(f))
		if m == nil {
			t.Errorf("%s: want sections/NN-topic.sh, or run-tests.sh does not run it", f)
			continue
		}
		if other, ok := seen[m[1]]; ok {
			t.Errorf("%s and %s: the same place in the order", other, f)
		}
		seen[m[1]] = f
		b, _ := os.ReadFile(f)
		if !regexp.MustCompile(`(?m)^section `).Match(b) {
			t.Errorf("%s: no section", f)
		}
	}
	lib, _ := os.ReadFile("lib.sh")
	for _, fn := range []string{"check", "section", "skip", "eventually", "write_results"} {
		if !regexp.MustCompile(`(?m)^` + fn + `\(\) `).Match(lib) {
			t.Errorf("lib.sh: no %s", fn)
		}
	}
	run, _ := os.ReadFile("run-tests.sh")
	for _, want := range []string{`. "$dir/lib.sh"`, `for f in "$dir"/sections/[0-9]*.sh; do`} {
		if !strings.Contains(string(run), want) {
			t.Errorf("run-tests.sh: no %q", want)
		}
	}
}

// The package installs what run-tests.sh sources.
func TestInstalled(t *testing.T) {
	for file, wants := range map[string][]string{
		"../../Makefile":                   {"test/vm/lib.sh $(DESTDIR)$(LIBEXECDIR)/custos/tests/lib.sh", "$(DESTDIR)$(LIBEXECDIR)/custos/tests/sections test/vm/sections/*.sh"},
		"../../packaging/suse/custos.spec": {"%{_libexecdir}/custos/tests/lib.sh", "%{_libexecdir}/custos/tests/sections"},
	} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range wants {
			if !strings.Contains(string(b), w) {
				t.Errorf("%s: no %q", file, w)
			}
		}
	}
}

func TestRefusesWithoutDisposable(t *testing.T) {
	for _, args := range [][]string{{}, {"--results", "/tmp/x"}, {"--bogus"}} {
		out, err := exec.Command("bash", append([]string{"run-tests.sh"}, args...)...).CombinedOutput()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 2 {
			t.Errorf("%v: %v (want exit status 2)\n%s", args, err, out)
		}
		if len(args) == 0 && !strings.Contains(string(out), "--disposable") {
			t.Errorf("no hint at --disposable: %s", out)
		}
	}
}
