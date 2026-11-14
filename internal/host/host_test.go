package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sdrahn/custos/internal/journal"
)

func TestHost(t *testing.T) {
	m := &journal.Memory{}
	h := &Host{Root: t.TempDir(), In: strings.NewReader("from stdin"), Journal: func(string) journal.Logger { return m }}
	if got := h.Path("/etc/custos"); got != filepath.Join(h.Root, "etc/custos") {
		t.Errorf("Path %q", got)
	}
	if h.Who() != "root" {
		t.Errorf("Who %q without By", h.Who())
	}
	h.By = "alice"
	if h.Who() != "alice" {
		t.Errorf("Who %q", h.Who())
	}
	// A file is written whole, with its mode, in a directory made for it;
	// "-" is the host's standard input.
	f := h.Path("/etc/custos/agents/pp/agent.yaml")
	if err := WriteFile(f, []byte("name: pp\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(f); st == nil || st.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", st)
	}
	if tmp, _ := filepath.Glob(filepath.Join(filepath.Dir(f), ".tmp-*")); len(tmp) != 0 {
		t.Errorf("temporary files left: %v", tmp)
	}
	if b, err := h.ReadFrom(f); err != nil || string(b) != "name: pp\n" {
		t.Errorf("ReadFrom file: %q %v", b, err)
	}
	if b, err := h.ReadFrom("-"); err != nil || string(b) != "from stdin" {
		t.Errorf("ReadFrom -: %q %v", b, err)
	}
	h.In = nil
	if _, err := h.ReadFrom("-"); err == nil {
		t.Error("ReadFrom - without a standard input")
	}
	// Root's record about an agent.
	h.Record(journal.CategoryPromoted, "pp", "promoted", journal.Fields{"CATEGORY": "patch-errata"})
	if r := m.Events(journal.CategoryPromoted); len(r) != 1 || r[0].Fields["CATEGORY"] != "patch-errata" || r[0].Message != "promoted" {
		t.Errorf("records %+v", r)
	}
	for name, ok := range map[string]bool{"patch-pilot": true, "_control": false, "../x": false, "": false} {
		if (CheckName(name) == nil) != ok {
			t.Errorf("CheckName(%q)", name)
		}
	}
}
