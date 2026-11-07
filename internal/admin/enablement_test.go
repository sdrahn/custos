package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sdrahn/custos/internal/journal"
)

const diagPerms = "- {server: systemd, tool: list_units, read_only: true, category: diagnostics}\n"

// A fresh host with a diagnostics agent becomes diagnostics-only.
func diagnosticsHost(t *testing.T) (*Admin, *journal.Memory) {
	t.Helper()
	a, mem, _ := withCategoriesRec(t)
	if err := a.SetPermissions("patch-pilot", strings.NewReader(diagPerms)); err != nil {
		t.Fatal(err)
	}
	if err := a.InitDiagnosticsOnly("setup"); err != nil {
		t.Fatal(err)
	}
	return a, mem
}

func records(mem *journal.Memory, event string) []journal.Record {
	var out []journal.Record
	for _, r := range mem.Records {
		if r.Event == event {
			out = append(out, r)
		}
	}
	return out
}

func TestDiagnosticsOnly(t *testing.T) {
	a, mem := diagnosticsHost(t)
	e, err := a.GetEnablement()
	if err != nil || e == nil || e.State != DiagnosticsOnly || len(e.Enabled) != 1 || e.Enabled[0] != Diagnostics || e.Record == "" {
		t.Fatalf("state %+v, %v", e, err)
	}
	b, _ := os.ReadFile(a.p(GatewayEnablement))
	if !strings.Contains(string(b), "_enablement:\n  state: diagnostics-only") {
		t.Errorf("gateway data:\n%s", b)
	}
	rec := records(mem, journal.CategoriesEnabled)
	if len(rec) != 1 || rec[0].Fields["ENABLEMENT"] != e.Record || rec[0].Fields["ENABLEMENT_SHA256"] != e.Digest() ||
		rec[0].Fields["BY"] != "setup" || !strings.Contains(rec[0].Fields["ENABLEMENT_TO"], "diagnostics-only") {
		t.Errorf("record %+v", rec)
	}
	// Not a way back.
	if err := a.InitDiagnosticsOnly("setup"); err == nil || !strings.Contains(err.Error(), "not offered") {
		t.Errorf("twice: %v", err)
	}
}

// Only a fresh host: an agent with other permissions keeps it from
// becoming diagnostics-only.
func TestDiagnosticsOnlyNeedsAFreshHost(t *testing.T) {
	a, _, _ := withCategoriesRec(t) // patch-pilot has patch-errata and config-lifecycle
	if err := a.InitDiagnosticsOnly("setup"); err == nil || !strings.Contains(err.Error(), "outside diagnostics") {
		t.Errorf("%v", err)
	}
	if _, err := os.Stat(a.p(GatewayEnablement)); !os.IsNotExist(err) {
		t.Error("written all the same")
	}
}

// On a diagnostics-only host, agent-admin refuses to load other
// categories, and records each refusal with the person.
func TestDisabledCategoriesRefused(t *testing.T) {
	a, mem := diagnosticsHost(t)
	a.By = "alice"
	for _, perms := range []string{
		"- {server: zypp, tool: patch, category: patch-errata}\n",
		"- {server: zypp, tool: 'list_*', read_only: true}\n", // without a category
	} {
		if err := a.SetPermissions("patch-pilot", strings.NewReader(perms)); err == nil || !strings.Contains(err.Error(), "diagnostics-only") {
			t.Errorf("%q: %v", perms, err)
		}
	}
	if err := promote(a, "config-lifecycle", sat); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Errorf("promote: %v", err)
	}
	rec := records(mem, journal.CategoryRefused)
	if len(rec) != 3 || rec[0].Fields["CATEGORY"] != "patch-errata" || rec[0].Fields["BY"] != "alice" ||
		rec[0].Fields["REASON"] != "permissions" || rec[2].Fields["REASON"] != "promote" {
		t.Errorf("records %+v", rec)
	}
	// diagnostics itself is fine.
	if err := a.SetPermissions("patch-pilot", strings.NewReader(diagPerms)); err != nil {
		t.Error(err)
	}
}

// diagnostics only reads, and is never promoted.
func TestDiagnosticsReadsOnly(t *testing.T) {
	a, _, _ := withCategoriesRec(t)
	err := a.SetPermissions("patch-pilot", strings.NewReader("- {server: systemd, tool: restart_unit, category: diagnostics}\n"))
	if err == nil || !strings.Contains(err.Error(), "read_only") {
		t.Errorf("%v", err)
	}
	a.SetPermissions("patch-pilot", strings.NewReader(diagPerms))
	if err := promote(a, Diagnostics, sat); err == nil || !strings.Contains(err.Error(), "never promoted") {
		t.Errorf("promote: %v", err)
	}
}

// enable is the one action: recorded, and categories start supervised:
// the agent data is what a broader install with them has.
func TestEnable(t *testing.T) {
	a, mem := diagnosticsHost(t)
	if err := a.Enable("alice", []string{"no-such"}, false); err == nil {
		t.Error("an unknown category enabled")
	}
	if err := a.Enable("alice", []string{"patch-errata"}, false); err != nil {
		t.Fatal(err)
	}
	e, _ := a.GetEnablement()
	if e.State != SomeCategories || strings.Join(e.Enabled, ",") != "diagnostics,patch-errata" {
		t.Errorf("after enable: %+v", e)
	}
	rec := records(mem, journal.CategoriesEnabled)
	last := rec[len(rec)-1]
	if last.Fields["BY"] != "alice" || !strings.Contains(last.Fields["ENABLEMENT_FROM"], "diagnostics-only") ||
		last.Fields["ENABLEMENT"] != e.Record || !strings.Contains(last.Message, "patch-errata") {
		t.Errorf("record %+v", last)
	}
	perms := "- {server: zypp, tool: patch, category: patch-errata}\n"
	if err := a.SetPermissions("patch-pilot", strings.NewReader(perms)); err != nil {
		t.Errorf("an enabled category: %v", err)
	}
	if err := a.SetPermissions("patch-pilot", strings.NewReader("- {server: x, tool: y, category: config-lifecycle}\n")); err == nil {
		t.Error("a category still disabled")
	}
	if err := a.Enable("alice", []string{"patch-errata"}, false); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("twice: %v", err)
	}

	// --all: every category, supervised; the agent's data is untouched.
	before, _ := os.ReadFile(a.gatewayFile("patch-pilot"))
	if err := a.Enable("alice", nil, true); err != nil {
		t.Fatal(err)
	}
	e, _ = a.GetEnablement()
	cats, _ := a.Categories()
	if len(e.Enabled) != len(cats) {
		t.Errorf("--all: %v of %d", e.Enabled, len(cats))
	}
	for c := range cats {
		if !e.Usable(c) {
			t.Errorf("%s not usable after --all", c)
		}
	}
	after, _ := os.ReadFile(a.gatewayFile("patch-pilot"))
	if string(before) != string(after) {
		t.Errorf("enable changed the agent's data:\n%s\n%s", before, after)
	}
	d, _ := a.readGateway("patch-pilot")
	if len(d.Categories) != 0 {
		t.Errorf("a category left supervised: %+v", d.Categories)
	}
}

// Without enablement data, every category is enabled already.
func TestEnableOnBroaderInstall(t *testing.T) {
	a, _, _ := withCategoriesRec(t)
	if err := a.Enable("alice", []string{"patch-errata"}, false); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("%v", err)
	}
	var e *Enablement
	for _, c := range []string{"", "patch-errata", Diagnostics} {
		if !e.Usable(c) {
			t.Errorf("%q not usable without enablement data", c)
		}
	}
}

// show reports the state, and a state changed without its record.
func TestShowEnablementTampered(t *testing.T) {
	a, mem := diagnosticsHost(t)
	var forged []string // records an agent wrote (its own _UID)
	journalOf := func(...string) ([]byte, error) {
		var out []string
		for _, r := range records(mem, journal.CategoriesEnabled) {
			b, _ := json.Marshal(map[string]string{"_UID": "0", "_COMM": "agent-admin", "AGENT_ENABLEMENT": r.Fields["ENABLEMENT"], "AGENT_ENABLEMENT_SHA256": r.Fields["ENABLEMENT_SHA256"]})
			out = append(out, string(b))
		}
		return []byte(strings.Join(append(out, forged...), "\n")), nil
	}
	a.JournalJSON = journalOf
	p, err := a.Show()
	if err != nil {
		t.Fatal(err)
	}
	if p.Enablement == nil || p.Enablement.State != DiagnosticsOnly || p.Enablement.Tampered != "" {
		t.Errorf("show: %+v", p.Enablement)
	}
	// Edited by hand: every category "enabled" without agent-admin.
	f := a.p(GatewayEnablement)
	b, _ := os.ReadFile(f)
	os.WriteFile(f, []byte(strings.Replace(strings.Replace(string(b), "state: diagnostics-only", "state: categories", 1),
		"- diagnostics", "- diagnostics\n    - patch-errata", 1)), 0o644)
	p, _ = a.Show()
	// The host is sealed (#109): show reports what the gateway enforces,
	// diagnostics-only, and both accounts of the edit.
	if p.Enablement.State != DiagnosticsOnly || !p.Enablement.Sealed || !strings.Contains(p.Enablement.Tampered, "does not match its seal") ||
		!strings.Contains(p.Enablement.Tampered, "differs from its record") || !strings.Contains(p.Enablement.Tampered, `"state":"categories"`) {
		t.Errorf("tampered: %+v", p.Enablement)
	}
	// An agent writes a record that matches the edited state: it is not
	// root's, so the state is still tampered.
	b2, _ := json.Marshal(map[string]string{"_UID": "981", "SYSLOG_IDENTIFIER": "custos-promotion", "AGENT_ENABLEMENT": p.Enablement.Record,
		"AGENT_ENABLEMENT_SHA256": (&Enablement{State: SomeCategories, Enabled: []string{Diagnostics, "patch-errata"}}).Digest()})
	forged = append(forged, string(b2))
	if p, _ = a.Show(); !strings.Contains(p.Enablement.Tampered, "differs from its record") {
		t.Errorf("an agent's record covered the edit: %+v", p.Enablement)
	}
	// Without a record id at all.
	os.WriteFile(f, []byte("_enablement:\n  state: categories\n  enabled: [diagnostics, patch-errata]\n"), 0o644)
	p, _ = a.Show()
	if !strings.Contains(p.Enablement.Tampered, "no record") {
		t.Errorf("no record: %+v", p.Enablement)
	}
}

// create --template refuses a template whose permissions name a disabled
// category.
func TestTemplateOfDisabledCategoryRefused(t *testing.T) {
	a, mem := diagnosticsHost(t)
	dir := a.p(filepath.Join(TemplatesSite, "patcher"))
	writeFile(filepath.Join(dir, "template.yaml"), []byte("title: Patcher\ndescription: x\nsystem: any\nmode: supervised\n"), 0o644)
	writeFile(filepath.Join(dir, "agent.yaml"), []byte(strings.Replace(yml, "patch-pilot", "patcher", 1)), 0o644)
	// As the shipped patch-pilot: read-only permissions without a category
	// first, then two disabled categories.
	writeFile(filepath.Join(dir, "permissions.yaml"), []byte("- {server: zypp, tool: check_updates, read_only: true}\n"+
		"- {server: zypp, tool: patch, category: patch-errata}\n- {server: systemd, tool: restart_unit, category: config-lifecycle}\n"+
		"- {server: zypp, tool: install, category: patch-errata}\n"), 0o644)
	err := a.CreateFromTemplate("patcher", "patcher", "")
	if err == nil || !strings.Contains(err.Error(), "task categories config-lifecycle, patch-errata and permissions without a category are not enabled") ||
		!strings.Contains(err.Error(), "agent-admin enable config-lifecycle patch-errata") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(a.agentDir("patcher")); !os.IsNotExist(err) {
		t.Error("created all the same")
	}
	// One record per disabled category, the one without a category too.
	rec := records(mem, journal.CategoryRefused)
	if len(rec) != 3 || rec[0].Fields["REASON"] != "create --template patcher" || rec[0].Fields["CATEGORY"] != "(none)" ||
		rec[1].Fields["CATEGORY"] != "patch-errata" || rec[2].Fields["CATEGORY"] != "config-lifecycle" {
		t.Errorf("records %+v", rec)
	}
}

// disable is the way back from enable (#110): refused while an agent has
// permissions in the category or has it promoted; then the gateway data
// lists every other category, the change is recorded, and enable undoes
// it. --all returns to diagnostics-only, once only diagnostics is used.
func TestDisable(t *testing.T) {
	a, mem := withCategories(t) // every category enabled; patch-pilot has patch-errata and config-lifecycle
	for _, c := range []struct {
		cats []string
		all  bool
		want string
	}{
		{nil, false, "name the categories"},
		{[]string{"no-such"}, false, "no-such"},
		{[]string{Diagnostics}, false, "always enabled"},
	} {
		if err := a.Disable("alice", c.cats, c.all); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.cats, err, c.want)
		}
	}
	err := a.Disable("alice", []string{"patch-errata"}, false)
	if err == nil || !strings.Contains(err.Error(), "agent patch-pilot has permissions in patch-errata (agent-admin permissions patch-pilot)") {
		t.Fatalf("with a permission in it: %v", err)
	}
	if r := records(mem, journal.CategoryRefused); len(r) != 1 || r[0].Fields["CATEGORY"] != "patch-errata" || r[0].Fields["REASON"] != "disable" || r[0].Fields["BY"] != "alice" {
		t.Errorf("category.refused %+v", r)
	}
	if e, _ := a.GetEnablement(); e != nil {
		t.Errorf("a refused disable wrote %+v", e)
	}

	// Promoted, without the permission: still refused, until demoted.
	if err := promote(a, "patch-errata", sat); err != nil {
		t.Fatal(err)
	}
	if err := a.SetPermissions("patch-pilot", strings.NewReader("- {server: systemd, tool: restart_unit, category: config-lifecycle}\n")); err != nil {
		t.Fatal(err)
	}
	if err := a.Disable("alice", []string{"patch-errata"}, false); err == nil || !strings.Contains(err.Error(), "has patch-errata promoted (agent-admin demote patch-pilot patch-errata)") {
		t.Fatalf("promoted: %v", err)
	}
	if err := a.Demote("patch-pilot", "patch-errata", "alice", "going back"); err != nil {
		t.Fatal(err)
	}
	if err := a.Disable("alice", []string{"patch-errata"}, false); err != nil {
		t.Fatal(err)
	}
	e, _ := a.GetEnablement()
	cats, _ := a.Categories()
	if e == nil || e.State != SomeCategories || len(e.Enabled) != len(cats)-1 || e.Usable("patch-errata") || !e.Usable("config-lifecycle") || !e.Usable("") {
		t.Fatalf("after disable: %+v", e)
	}
	rec := records(mem, journal.CategoriesDisabled)
	if len(rec) != 1 || rec[0].Fields["ENABLEMENT"] != e.Record || rec[0].Fields["ENABLEMENT_SHA256"] != e.Digest() ||
		rec[0].Fields["BY"] != "alice" || rec[0].Fields["ENABLEMENT_FROM"] != `{"state":"","enabled":"all"}` || !strings.Contains(rec[0].Message, "patch-errata") {
		t.Errorf("categories.disabled %+v", rec)
	}
	// Disabled now: refused as any disabled category; enable undoes it.
	if err := a.SetPermissions("patch-pilot", strings.NewReader("- {server: zypp, tool: patch, category: patch-errata}\n")); err == nil {
		t.Error("a permission in a disabled category")
	}
	if err := a.Disable("alice", []string{"patch-errata"}, false); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Errorf("twice: %v", err)
	}
	if err := a.Enable("alice", []string{"patch-errata"}, false); err != nil {
		t.Fatal(err)
	}

	// --all: diagnostics-only, once the agent has only diagnostics.
	if err := a.Disable("alice", nil, true); err == nil || !strings.Contains(err.Error(), "permissions in config-lifecycle") {
		t.Fatalf("--all with other permissions: %v", err)
	}
	if err := a.SetPermissions("patch-pilot", strings.NewReader(diagPerms)); err != nil {
		t.Fatal(err)
	}
	if err := a.Disable("alice", nil, true); err != nil {
		t.Fatal(err)
	}
	if e, _ := a.GetEnablement(); e.State != DiagnosticsOnly || strings.Join(e.Enabled, ",") != Diagnostics || e.Usable("") {
		t.Errorf("after --all: %+v", e)
	}
	if err := a.Disable("alice", []string{"patch-errata"}, false); err == nil || !strings.Contains(err.Error(), "diagnostics-only already") {
		t.Errorf("on diagnostics-only: %v", err)
	}
	// And forward again, as from the bundled install.
	if err := a.Enable("alice", []string{"patch-errata"}, false); err != nil {
		t.Errorf("enable after going back: %v", err)
	}
}

// A state disable wrote is root's, recorded: show does not call it
// tampered.
func TestDisableRecordNotTampered(t *testing.T) {
	a, mem := diagnosticsHost(t)
	if err := a.Enable("alice", []string{"patch-errata"}, false); err != nil {
		t.Fatal(err)
	}
	if err := a.Disable("alice", []string{"patch-errata"}, false); err != nil {
		t.Fatal(err)
	}
	var asked []string
	a.JournalJSON = func(args ...string) ([]byte, error) {
		asked = args
		var out []string
		for _, ev := range []string{journal.CategoriesEnabled, journal.CategoriesDisabled} {
			for _, r := range records(mem, ev) {
				b, _ := json.Marshal(map[string]string{"_UID": "0", "_COMM": "agent-admin", "AGENT_ENABLEMENT": r.Fields["ENABLEMENT"], "AGENT_ENABLEMENT_SHA256": r.Fields["ENABLEMENT_SHA256"]})
				out = append(out, string(b))
			}
		}
		return []byte(strings.Join(out, "\n")), nil
	}
	p, err := a.Show()
	if err != nil {
		t.Fatal(err)
	}
	if p.Enablement.Tampered != "" || !strings.Contains(strings.Join(asked, " "), "AGENT_EVENT="+journal.CategoriesDisabled) {
		t.Errorf("tampered %q, asked %v", p.Enablement.Tampered, asked)
	}
}

// rootJournal is the enablement records as journalctl gives them,
// agent-admin's as root, oldest first, with every field it wrote.
func rootJournal(mem *journal.Memory) func(...string) ([]byte, error) {
	return func(...string) ([]byte, error) {
		var out []string
		for _, r := range mem.Records {
			if r.Event != journal.CategoriesEnabled && r.Event != journal.CategoriesDisabled {
				continue
			}
			m := map[string]string{"_UID": "0", "_COMM": "agent-admin", "AGENT_EVENT": r.Event}
			for k, v := range r.Fields {
				m["AGENT_"+k] = v
			}
			b, _ := json.Marshal(m)
			out = append(out, string(b))
		}
		return []byte(strings.Join(out, "\n")), nil
	}
}

// The seal covers the state, not when it was written, nor itself: show
// reports an earlier signed state put back, and a seal removed from a
// host agent-admin sealed (security review M5).
func TestShowReplayAndRemovedSeal(t *testing.T) {
	a, mem := diagnosticsHost(t)
	a.JournalJSON = rootJournal(mem)
	f := a.p(GatewayEnablement)
	earlier, _ := os.ReadFile(f)                           // diagnostics-only, signed
	a.Secret = func() string { return "fedcba9876543210" } // the next record's id
	if err := a.Enable("alice", []string{"patch-errata"}, false); err != nil {
		t.Fatal(err)
	}
	if p, _ := a.Show(); p.Enablement.Tampered != "" || p.Enablement.State != SomeCategories {
		t.Fatalf("after enable: %+v", p.Enablement)
	}
	// The earlier state put back: its signature verifies, its record is not
	// the latest.
	os.WriteFile(f, earlier, 0o644)
	p, _ := a.Show()
	if !p.Enablement.Sealed || !strings.Contains(p.Enablement.Tampered, "an earlier state was put back") {
		t.Errorf("replayed: %+v", p.Enablement)
	}
	// The seal removed: the gateway counts the state as it is.
	os.Remove(a.p(GatewaySeal))
	p, _ = a.Show()
	if p.Enablement == nil || !strings.Contains(p.Enablement.Tampered, "was removed, though agent-admin sealed this host") {
		t.Errorf("seal removed: %+v", p.Enablement)
	}
	// And the state too: every category, and said so.
	os.Remove(f)
	p, _ = a.Show()
	if p.Enablement == nil || p.Enablement.State != SomeCategories || !slices.Contains(p.Enablement.Enabled, "patch-errata") ||
		!strings.Contains(p.Enablement.Tampered, "no state: every category") {
		t.Errorf("seal and state removed: %+v", p.Enablement)
	}
	// A host never sealed (no SEALED record) without a seal: nothing to say.
	b, _ := newAdmin(t)
	b.JournalJSON = func(...string) ([]byte, error) { return nil, nil }
	if p, err := b.Show(); err != nil || p.Enablement != nil {
		t.Errorf("never sealed: %+v %v", p, err)
	}
}

// A diagnostics permission names its tool: a pattern would allow what
// changes the system on a diagnostics-only host too (security review M5).
func TestDiagnosticsNamesEachTool(t *testing.T) {
	a, _ := diagnosticsHost(t)
	for _, bad := range []string{
		"- {server: '*', tool: '*', category: diagnostics, read_only: true}\n",
		"- {server: zypp, tool: '*', category: diagnostics, read_only: true}\n",
		"- {server: zypp, tool: 'list_*', category: diagnostics, read_only: true}\n",
		"- {server: zypp, category: diagnostics, read_only: true}\n",
	} {
		if err := a.SetPermissions("patch-pilot", strings.NewReader(bad)); err == nil || !strings.Contains(err.Error(), "names each tool") {
			t.Errorf("%s: %v", bad, err)
		}
	}
}
