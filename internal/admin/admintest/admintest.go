// Package admintest is agent-admin on a test host (hosttest), for the
// tests of the packages built on internal/admin.
package admintest

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/category"
	"github.com/sdrahn/custos/internal/host"
	"github.com/sdrahn/custos/internal/host/hosttest"
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
