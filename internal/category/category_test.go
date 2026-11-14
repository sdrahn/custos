package category

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shipped is a root with the packages' catalog (packaging/categories).
func shipped(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	m, err := filepath.Glob("../../packaging/categories/*.yaml")
	if err != nil || len(m) == 0 {
		t.Fatalf("no shipped categories: %v", err)
	}
	for _, f := range m {
		b, _ := os.ReadFile(f)
		write(t, filepath.Join(root, Vendor, filepath.Base(f)), string(b))
	}
	return root
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestShippedCatalog(t *testing.T) {
	root := shipped(t)
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"patch-errata", "security-response", "config-lifecycle", "diagnostics", "compliance-audit"} {
		if c[n].Title == "" {
			t.Errorf("%s: missing or without a title: %v", n, c)
		}
	}
	if err := Check(root, "patch-errata"); err != nil {
		t.Error(err)
	}
	if err := Check(root, "no-such"); err == nil || !strings.Contains(err.Error(), `no task category "no-such" (the catalog has: compliance-audit, config-lifecycle`) {
		t.Errorf("an unknown category: %v", err)
	}
}

// The administrator's file replaces the package's of the same name; an
// empty one disables it; "uncategorized" is no category.
func TestSiteCatalogReplacesAndDisables(t *testing.T) {
	root := shipped(t)
	write(t, filepath.Join(root, Site, "patch-errata.yaml"), "patch-errata: {title: Patches (site)}\nbackups: {title: Backups}\n")
	write(t, filepath.Join(root, Site, "security-response.yaml"), "")
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if c["patch-errata"].Title != "Patches (site)" || c["backups"].Title != "Backups" {
		t.Errorf("site entries: %v", c)
	}
	if _, ok := c["security-response"]; ok {
		t.Error("an empty site file did not disable security-response")
	}
	write(t, filepath.Join(root, Site, "bad.yaml"), "uncategorized: {title: x}\n")
	if _, err := Load(root); err == nil {
		t.Error("uncategorized accepted as a category")
	}
}

// An envelope is checked whole: what nothing enforces is refused, never
// ignored. Its hash does not depend on how it was written.
func TestParseEnvelope(t *testing.T) {
	const sat = "windows:\n  - {days: [sat], from: '02:00', to: '04:00', tz: Europe/Berlin}\n"
	e, err := ParseEnvelope([]byte(sat + "rate: {calls: 20, per: 1h}\nrollback: {on: [tool_error, health_check_failed], health_check: 'systemctl is-system-running'}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Windows) != 1 || e.Windows[0].Days[0] != "sat" || e.Rate.Calls != 20 || e.Rollback.HealthCheck == "" {
		t.Errorf("envelope %+v", e)
	}
	for in, says := range map[string]string{
		"windows: []\n": "no windows",
		"windows: [{from: '2:00', to: '04:00'}]\n":                    "HH:MM",
		"windows: [{from: '04:00', to: '02:00'}]\n":                   "is not before",
		"windows: [{days: [sunday], from: '02:00', to: '04:00'}]\n":   `day "sunday"`,
		"windows: [{from: '02:00', to: '04:00', tz: Mars/Olympus}]\n": `tz "Mars/Olympus"`,
		sat + "rate: {calls: 20, per: 1d}\n":                          `per "1d": want 1s, 1m or 1h`,
		sat + "rate: {per: 1h}\n":                                     "calls 0: want 1 or more",
		sat + "rollback: {on: []}\n":                                  "rollback: no conditions",
		sat + "rollback: {on: [disk_full]}\n":                         `condition "disk_full"`,
		sat + "rollback: {on: [health_check_failed]}\n":               "go together",
		sat + "hosts: [web1]\n":                                       "parked (CONTRACT.md O13)",
		sat + "whenever: true\n":                                      `unknown key "whenever"`,
	} {
		if _, err := ParseEnvelope([]byte(in)); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%q: %v, want %q", in, err, says)
		}
	}
	other, _ := ParseEnvelope([]byte("windows: [{tz: Europe/Berlin, to: '04:00', from: '02:00', days: [sat]}]\n"))
	plain, _ := ParseEnvelope([]byte(sat))
	if h := EnvelopeHash(plain); len(h) != 64 || h != EnvelopeHash(other) || h == EnvelopeHash(e) || EnvelopeHash(nil) != "" {
		t.Errorf("hashes %q %q %q", h, EnvelopeHash(other), EnvelopeHash(e))
	}
}
