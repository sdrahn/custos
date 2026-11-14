// Package boundaries checks which package may import which
// (docs/architecture.md): the layers, the two sides of the socket, where
// the agent host and each third-party module are used. A new package gets
// its place in the tables here and its line in docs/architecture.md.
package boundaries

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/sdrahn/custos/"

// layers: a package imports only packages of the layers before its own.
var layers = [][]string{
	// Leaves: nothing of this module.
	{"internal/journal", "internal/peercred", "internal/playbook", "internal/gateway", "internal/gatewayctl", "internal/clienttools", "internal/openai", "internal/readiness", "internal/testutil"},
	{"internal/config"},
	{"internal/category", "internal/delegate", "internal/openshell", "internal/host", "internal/governance", "internal/relay"},
	{"internal/gatewayconf", "internal/agui", "internal/host/hosttest"},
	{"internal/launcher", "internal/admin"},
	{"internal/admin/idp", "internal/admin/templates", "internal/admin/promotion", "internal/admin/admintest"},
	{"internal/admin/setup"},
	{"cmd/agent-launcher", "cmd/agent-delegate", "cmd/agent-openshell-relay", "cmd/agent-admin", "cmd/custos-setup"},
}

// The agent's process and root's tools meet at the agent's socket and in
// the journal (CONTRACT.md §5, §10), never in code: neither side imports
// the other. What both use (config, journal, playbook, delegate, peercred)
// is on neither list.
var (
	agentSide = []string{"internal/launcher", "internal/governance", "internal/agui", "internal/clienttools", "internal/openai", "internal/gateway", "cmd/agent-launcher"}
	adminSide = []string{"internal/admin", "internal/host", "internal/category", "internal/gatewayconf", "internal/gatewayctl", "internal/readiness", "cmd/agent-admin", "cmd/custos-setup"}
)

// owners: the packages that may import a third-party module (by prefix).
// The agent host (ADK) stays out of everything Part I of the contract is
// implemented by; one package wraps each module that replaces code of
// our own.
var owners = map[string][]string{
	"google.golang.org/adk":            {"internal/launcher", "internal/governance", "internal/agui", "internal/clienttools", "internal/openai", "internal/testutil"},
	"google.golang.org/genai":          {"internal/launcher", "internal/governance", "internal/agui", "internal/clienttools", "internal/openai", "internal/testutil"},
	"github.com/modelcontextprotocol/": {"internal/launcher", "internal/governance", "internal/gateway", "internal/delegate"},
	"github.com/openai/":               {"internal/openai"},
	"github.com/coreos/go-systemd/":    {"internal/journal"},
	"golang.org/x/oauth2":              {"internal/admin/idp"},
	"github.com/golang-jwt/":           {"internal/admin/idp"},
	"github.com/aymanbagabas/go-udiff": {"internal/admin/templates"},
	"gorm.io/":                         {"internal/launcher"},
	"github.com/glebarez/sqlite":       {"internal/launcher"},
	"github.com/google/uuid":           {"internal/agui", "internal/openai"},
	"golang.org/x/net":                 {"internal/gatewayconf"},
	"gopkg.in/yaml.v3":                 {"internal/config", "internal/category", "internal/playbook", "internal/openshell", "internal/gatewayconf", "internal/admin"},
}

// testOnly are helpers for tests: only test files import them (and each
// other).
var testOnly = []string{"internal/host/hosttest", "internal/admin/admintest", "internal/testutil"}

type imp struct {
	file, path string
	test       bool
}

func root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// imports are the imports of every package below internal/ and cmd/, by
// package.
func imports(t *testing.T) map[string][]imp {
	t.Helper()
	out := map[string][]imp{}
	fset := token.NewFileSet()
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root(), top), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
				return err
			}
			f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root(), p)
			pkg := filepath.ToSlash(filepath.Dir(rel))
			out[pkg] = append(out[pkg], nil...)
			for _, s := range f.Imports {
				path, _ := strconv.Unquote(s.Path.Value)
				out[pkg] = append(out[pkg], imp{filepath.ToSlash(rel), path, strings.HasSuffix(p, "_test.go")})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func in(list []string, pkg string) bool {
	for _, x := range list {
		if pkg == x || strings.HasPrefix(pkg, x+"/") {
			return true
		}
	}
	return false
}

func TestLayers(t *testing.T) {
	layer := map[string]int{}
	for i, l := range layers {
		for _, p := range l {
			layer[p] = i
		}
	}
	all := imports(t)
	for pkg, imps := range all {
		at, ok := layer[pkg]
		if !ok {
			t.Errorf("%s: a new package: give it a layer in test/boundaries and a line in docs/architecture.md", pkg)
			continue
		}
		for _, i := range imps {
			dep, ours := strings.CutPrefix(i.path, module)
			if !ours || i.test {
				continue
			}
			if to, ok := layer[dep]; ok && to >= at {
				t.Errorf("%s imports %s, which is not below it (docs/architecture.md)", i.file, dep)
			}
		}
	}
	for p := range layer {
		if _, ok := all[p]; !ok {
			t.Errorf("%s: in the layers, but not in the tree", p)
		}
	}
}

func TestSides(t *testing.T) {
	for pkg, imps := range imports(t) {
		for _, i := range imps {
			dep, ours := strings.CutPrefix(i.path, module)
			if !ours {
				continue
			}
			if in(agentSide, pkg) && in(adminSide, dep) {
				t.Errorf("%s: the agent's process imports %s, which is root's", i.file, dep)
			}
			if in(adminSide, pkg) && in(agentSide, dep) {
				t.Errorf("%s: root's tools import %s, which is the agent's process", i.file, dep)
			}
		}
	}
}

func TestModuleOwners(t *testing.T) {
	for pkg, imps := range imports(t) {
		for _, i := range imps {
			if strings.HasPrefix(i.path, module) || !strings.Contains(strings.SplitN(i.path, "/", 2)[0], ".") {
				continue // ours, or the standard library
			}
			known := false
			for prefix, who := range owners {
				if !strings.HasPrefix(i.path, prefix) {
					continue
				}
				known = true
				if !in(who, pkg) {
					t.Errorf("%s imports %s; it is used in %s only (docs/architecture.md)", i.file, i.path, strings.Join(who, ", "))
				}
			}
			if !known {
				t.Errorf("%s imports %s: a new module: say in test/boundaries and docs/architecture.md which package uses it", i.file, i.path)
			}
		}
	}
}

func TestHelpersInTestsOnly(t *testing.T) {
	for pkg, imps := range imports(t) {
		for _, i := range imps {
			dep, ours := strings.CutPrefix(i.path, module)
			if ours && in(testOnly, dep) && !i.test && !in(testOnly, pkg) {
				t.Errorf("%s imports %s, a helper for tests", i.file, dep)
			}
		}
	}
}

// The architecture document names every package and every module with an
// owner.
func TestDocumented(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(root(), "docs", "architecture.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	var missing []string
	for pkg := range imports(t) {
		if !strings.Contains(doc, "`"+pkg+"`") {
			missing = append(missing, pkg)
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("docs/architecture.md does not name `%s`", m)
	}
}
