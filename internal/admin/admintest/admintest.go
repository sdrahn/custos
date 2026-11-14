// Package admintest is agent-admin on a test host (hosttest), for the
// tests of the packages built on internal/admin.
package admintest

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/category"
	"github.com/sdrahn/custos/internal/host"
	"github.com/sdrahn/custos/internal/host/hosttest"
	"github.com/sdrahn/custos/internal/journal"
)

// New is agent-admin on a test host, and what the host was asked to do.
func New(t *testing.T) (*admin.Admin, *hosttest.Rec) {
	t.Helper()
	h, r := hosttest.New(t)
	r.Credential = admin.SealCredential // the seal's key (#109)
	a := &admin.Admin{
		Host: h,
		Control: func(sock, path string) error {
			r.Cmds = append(r.Cmds, "POST "+sock+path)
			return nil
		},
		Start: func(sock string, body map[string]any) error {
			o := body["origin"].(map[string]any)
			r.Cmds = append(r.Cmds, "START "+sock+" "+body["prompt"].(string)+" "+o["kind"].(string)+" "+o["schedule"].(string)+" "+o["by"].(string))
			return nil
		},
		// No DNS in tests: names the test resolves are the test's.
		LookupHost:  func(h string) ([]string, error) { return nil, errors.New("no such host") },
		Image:       "localhost/agent:test",
		Secret:      func() string { return "0123456789abcdef0123456789abcdef" },
		SandboxGone: 5 * time.Second,
	}
	return a, r
}

// Repo is a path of the source tree (its shipped categories, templates
// and playbooks), wherever the test runs.
func Repo(elem ...string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(append([]string{filepath.Dir(file), "..", "..", ".."}, elem...)...)
}

// Categories installs the shipped category catalog on a's host.
func Categories(t *testing.T, a *admin.Admin) {
	t.Helper()
	m, err := filepath.Glob(Repo("packaging", "categories", "*.yaml"))
	if err != nil || len(m) == 0 {
		t.Fatalf("no shipped categories: %v", err)
	}
	for _, f := range m {
		b, _ := os.ReadFile(f)
		host.WriteFile(a.Path(filepath.Join(category.Vendor, filepath.Base(f))), b, 0o644)
	}
}

// CopyTree copies the files below from to to.
func CopyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return host.WriteFile(filepath.Join(to, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Yml is the configuration of the agent patch-pilot of WithAgent.
const Yml = `version: 1
name: patch-pilot
model: {provider: openai, name: m, endpoint: "http://ai:8000/v1"}
gateway: {servers: [zypp]}
schedules:
  - {name: compliance, on_calendar: "Mon..Fri 06:00", prompt: "Run the compliance check."}
`

// WithAgent is agent-admin with the shipped categories and the agent
// patch-pilot, which has a read-only permission and one each in
// patch-errata and config-lifecycle; root's records go to the memory
// returned, which starts empty.
func WithAgent(t *testing.T) (*admin.Admin, *journal.Memory, *hosttest.Rec) {
	t.Helper()
	a, r := New(t)
	Categories(t, a)
	mem := &journal.Memory{}
	a.Journal = func(string) journal.Logger { return mem }
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(Yml), 0o644)
	if err := a.Create("patch-pilot", f); err != nil {
		t.Fatal(err)
	}
	perms := "- {server: zypp, tool: 'list_*', read_only: true}\n- {server: zypp, tool: install_patches, category: patch-errata}\n- {server: systemd, tool: restart_unit, category: config-lifecycle}\n"
	if err := a.SetPermissions("patch-pilot", strings.NewReader(perms)); err != nil {
		t.Fatal(err)
	}
	mem.Records = nil
	return a, mem, r
}
