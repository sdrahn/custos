package admin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sdrahn/custos/internal/category"
	"github.com/sdrahn/custos/internal/host"
	"github.com/sdrahn/custos/internal/journal"
	"github.com/sdrahn/custos/internal/readiness"
)

// catalog writes the shipped categories (packaging/categories) into the
// test root, and an agent with permissions in two of them.
func withCategories(t *testing.T) (*Admin, *journal.Memory) {
	t.Helper()
	a, mem, _ := withCategoriesRec(t)
	return a, mem
}

// withCategoriesRec is withCategories with the commands agent-admin ran.
func withCategoriesRec(t *testing.T) (*Admin, *journal.Memory, *rec) {
	t.Helper()
	a, r := newAdmin(t)
	m, err := filepath.Glob("../../packaging/categories/*.yaml")
	if err != nil || len(m) == 0 {
		t.Fatalf("no shipped categories: %v", err)
	}
	for _, f := range m {
		b, _ := os.ReadFile(f)
		host.WriteFile(a.Path(filepath.Join(category.Vendor, filepath.Base(f))), b, 0o644)
	}
	mem := &journal.Memory{}
	a.Journal = func(string) journal.Logger { return mem }
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(yml), 0o644)
	if err := a.Create("patch-pilot", f); err != nil {
		t.Fatal(err)
	}
	perms := "- {server: zypp, tool: 'list_*', read_only: true}\n- {server: zypp, tool: install_patches, category: patch-errata}\n- {server: systemd, tool: restart_unit, category: config-lifecycle}\n"
	if err := a.SetPermissions("patch-pilot", strings.NewReader(perms)); err != nil {
		t.Fatal(err)
	}
	if r := mem.Events(journal.PermissionsChanged); len(r) != 1 || r[0].Fields["PERMISSIONS_FROM"] != "[]" { // create gave none
		t.Fatalf("permissions records: %v", r)
	}
	mem.Records = nil
	return a, mem, r
}

const sat = "windows:\n  - {days: [sat], from: '02:00', to: '04:00', tz: Europe/Berlin}\n"

func promote(a *Admin, cat, env string) error {
	return a.Promote("patch-pilot", cat, "alice", Promotion{Envelope: []byte(env), Reason: "patching window", DespiteReadiness: true})
}

func TestPermissionsCategory(t *testing.T) {
	a, _ := withCategories(t)
	for _, c := range []struct{ perms, says string }{
		{"- {server: zypp, tool: x, category: no-such}\n", `no task category "no-such"`},
		{"- {server: zypp, tool: x, category: [a]}\n", "category is a name"},
		{"- {server: zypp, tool: x, category: patch-errata, autonomous: true}\n", "autonomous is for permissions without a category"},
	} {
		if err := a.SetPermissions("patch-pilot", strings.NewReader(c.perms)); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%q: %v, want %q", c.perms, err, c.says)
		}
	}
}

func TestPromoteAndDemote(t *testing.T) {
	a, mem := withCategories(t)
	if err := promote(a, "patch-errata", sat); err != nil {
		t.Fatal(err)
	}
	d, _ := a.readGateway("patch-pilot")
	c := d.Categories["patch-errata"]
	if c == nil || c.Mode != "bounded" || c.PromotedBy != "alice 2026-11-03T12:00:00Z" || !strings.HasPrefix(c.Promotion, "p-") || len(c.Envelope.Windows) != 1 {
		t.Fatalf("after promote: %+v", c)
	}
	if d.Categories["config-lifecycle"] != nil || d.Mode != "supervised" {
		t.Errorf("promotion touched more than its category: %+v", d)
	}
	// The data as the gateway's policy reads it.
	b, _ := os.ReadFile(a.gatewayFile("patch-pilot"))
	var doc map[string]map[string]any
	yaml.Unmarshal(b, &doc)
	got := doc["patch-pilot"]["categories"].(map[string]any)["patch-errata"].(map[string]any)
	w := got["envelope"].(map[string]any)["windows"].([]any)[0].(map[string]any)
	if got["mode"] != "bounded" || w["tz"] != "Europe/Berlin" || w["from"] != "02:00" || w["to"] != "04:00" {
		t.Errorf("gateway data: %v", got)
	}
	r := mem.Events(journal.CategoryPromoted)
	if len(r) != 1 {
		t.Fatalf("promotion records: %v", mem.Records)
	}
	f := r[0].Fields
	if f["CATEGORY"] != "patch-errata" || f["MODE_FROM"] != "supervised" || f["MODE_TO"] != "bounded" || f["BY"] != "alice" ||
		f["REASON"] != "patching window" || f["PROMOTION"] != c.Promotion || len(f["ENVELOPE_SHA256"]) != 64 {
		t.Errorf("promotion record: %v", f)
	}
	// Other changes keep the category.
	if err := a.SetMode("patch-pilot", "supervised"); err != nil {
		t.Fatal(err)
	}
	if err := a.Pause("patch-pilot", "bob", true); err != nil {
		t.Fatal(err)
	}
	if d, _ := a.readGateway("patch-pilot"); d.Categories["patch-errata"].Mode != "bounded" {
		t.Error("mode or pause dropped the category")
	}
	if err := promote(a, "patch-errata", sat); err == nil || !strings.Contains(err.Error(), "already bounded") {
		t.Errorf("promoted twice with the same envelope: %v", err)
	}
	if err := a.Demote("patch-pilot", "patch-errata", "bob", "override rate"); err != nil {
		t.Fatal(err)
	}
	d, _ = a.readGateway("patch-pilot")
	if c := d.Categories["patch-errata"]; c.Mode != "supervised" || c.DemotedBy != "bob 2026-11-03T12:00:00Z" || len(c.Envelope.Windows) != 1 {
		t.Errorf("after demote: %+v", c)
	}
	if r := mem.Events(journal.CategoryDemoted); len(r) != 1 || r[0].Fields["BY"] != "bob" || r[0].Fields["MODE_TO"] != "supervised" || r[0].Fields["REASON"] != "override rate" {
		t.Errorf("demotion records: %v", r)
	}
	if err := a.Demote("patch-pilot", "patch-errata", "bob", ""); err == nil {
		t.Error("demoted a supervised category")
	}
}

func TestPromoteRefuses(t *testing.T) {
	a, mem := withCategories(t)
	for _, c := range []struct {
		name string
		do   func() error
		says string
	}{
		{"unknown category", func() error { return promote(a, "no-such", sat) }, `no task category "no-such"`},
		{"no permission in it", func() error { return promote(a, "security-response", sat) }, "no permission in category security-response"},
		{"no readiness", func() error {
			return a.Promote("patch-pilot", "patch-errata", "alice", Promotion{Envelope: []byte(sat)})
		}, "category patch-errata is not ready (observed 0.0 days of 14; 0 proposals of 50)"},
		{"no reason", func() error {
			return a.Promote("patch-pilot", "patch-errata", "alice", Promotion{Envelope: []byte(sat), DespiteReadiness: true})
		}, "needs --reason"},
		{"no windows", func() error { return promote(a, "patch-errata", "windows: []\n") }, "no windows"},
		{"rate per day", func() error { return promote(a, "patch-errata", sat+"rate: {calls: 20, per: 1d}\n") }, `per "1d": want 1s, 1m or 1h`},
		{"rate per 2h", func() error { return promote(a, "patch-errata", sat+"rate: {calls: 20, per: 2h}\n") }, `per "2h"`},
		{"rate without calls", func() error { return promote(a, "patch-errata", sat+"rate: {per: 1h}\n") }, "calls 0: want 1 or more"},
		{"rate key", func() error { return promote(a, "patch-errata", sat+"rate: {calls: 5, per: 1h, burst: 2}\n") }, "burst"},
		{"rollback without conditions", func() error { return promote(a, "patch-errata", sat+"rollback: {on: []}\n") }, "rollback: no conditions"},
		{"unknown rollback condition", func() error { return promote(a, "patch-errata", sat+"rollback: {on: [disk_full]}\n") }, `condition "disk_full"`},
		{"health check without a command", func() error { return promote(a, "patch-errata", sat+"rollback: {on: [health_check_failed]}\n") }, "health_check_failed and health_check (a command) go together"},
		{"a command without health_check_failed", func() error {
			return promote(a, "patch-errata", sat+"rollback: {on: [tool_error], health_check: 'true'}\n")
		}, "go together"},
		{"hosts", func() error { return promote(a, "patch-errata", sat+"hosts: [web1]\n") }, "parked (CONTRACT.md O13)"},
		{"blast radius", func() error { return promote(a, "patch-errata", sat+"blast_radius: {hosts: 2}\n") }, "parked (CONTRACT.md O13)"},
		{"unknown key", func() error { return promote(a, "patch-errata", sat+"whenever: true\n") }, `unknown key "whenever"`},
		{"bad time", func() error { return promote(a, "patch-errata", "windows: [{from: '2:00', to: '04:00'}]\n") }, "HH:MM"},
		{"across midnight", func() error { return promote(a, "patch-errata", "windows: [{from: '22:00', to: '02:00'}]\n") }, "is not before to"},
		{"bad day", func() error {
			return promote(a, "patch-errata", "windows: [{days: [saturday], from: '02:00', to: '04:00'}]\n")
		}, `day "saturday"`},
		{"bad zone", func() error {
			return promote(a, "patch-errata", "windows: [{from: '02:00', to: '04:00', tz: Mars/Olympus}]\n")
		}, `tz "Mars/Olympus"`},
		{"window key", func() error {
			return promote(a, "patch-errata", "windows: [{from: '02:00', to: '04:00', every: day}]\n")
		}, "every"},
	} {
		if err := c.do(); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v, want %q", c.name, err, c.says)
		}
	}
	if err := a.SetMode("patch-pilot", "advisory"); err != nil {
		t.Fatal(err)
	}
	if err := promote(a, "patch-errata", sat); err == nil || !strings.Contains(err.Error(), "advisory, which caps every category") {
		t.Errorf("advisory agent: %v", err)
	}
	if len(mem.Records) != 0 {
		t.Errorf("a refused promotion was recorded: %v", mem.Records)
	}
	if d, _ := a.readGateway("patch-pilot"); d.Categories != nil {
		t.Errorf("a refused promotion was written: %v", d.Categories)
	}
}

func TestPromoteWithRate(t *testing.T) {
	a, _ := withCategories(t)
	if err := promote(a, "patch-errata", sat+"rate: {calls: 20, per: 1h}\n"); err != nil {
		t.Fatal(err)
	}
	d, _ := a.readGateway("patch-pilot")
	if r := d.Categories["patch-errata"].Envelope.Rate; r == nil || r.Calls != 20 || r.Per != "1h" {
		t.Errorf("rate: %+v", r)
	}
	b, _ := os.ReadFile(a.Path(GatewayAgents + "/patch-pilot.yaml"))
	if !strings.Contains(string(b), "rate:\n") || !strings.Contains(string(b), "calls: 20") || !strings.Contains(string(b), "per: 1h") {
		t.Errorf("agent data:\n%s", b)
	}
}

func TestShowHasCategories(t *testing.T) {
	a, _ := withCategories(t)
	if err := promote(a, "patch-errata", sat); err != nil {
		t.Fatal(err)
	}
	p, err := a.Show("patch-pilot")
	if err != nil {
		t.Fatal(err)
	}
	if c := p.Agents["patch-pilot"].Categories["patch-errata"]; c == nil || c.Mode != "bounded" {
		t.Errorf("show: %+v", p.Agents["patch-pilot"])
	}
}

// promote waits, as mode does, until the gateway's OPA has the change.
func TestPromoteWaitsForThePolicy(t *testing.T) {
	a, _ := withCategories(t)
	var data map[string]any
	a.OPAData = func(string) (any, error) { return data, nil }
	a.PolicyWait = 0
	if err := promote(a, "patch-errata", sat); err == nil || !strings.Contains(err.Error(), "did not load the change") {
		t.Errorf("not loaded: %v", err)
	}
	d, _ := a.readGateway("patch-pilot")
	id := d.Categories["patch-errata"].Promotion
	data = map[string]any{"mode": "supervised", "paused": false, "permissions": []any{1, 2, 3},
		"categories": map[string]any{"patch-errata": map[string]any{"mode": "bounded", "promotion": id}}}
	if err := a.Demote("patch-pilot", "patch-errata", "bob", ""); err == nil {
		t.Error("demote returned while the gateway still had bounded")
	}
	// As with mode: the file is written, the gateway is late.
	if d, _ := a.readGateway("patch-pilot"); d.Categories["patch-errata"].Mode != "supervised" {
		t.Error("the demotion was not written")
	}
	// Once the gateway has it, the next change returns at once.
	data["categories"] = map[string]any{"patch-errata": map[string]any{"mode": "supervised", "promotion": id}}
	if err := a.SetMode("patch-pilot", "supervised"); err != nil {
		t.Errorf("loaded: %v", err)
	}
}

// Promoting again keeps the mode and changes the envelope: promote waits
// for the new promotion, not only for bounded.
func TestPromoteAgainWaitsForTheNewEnvelope(t *testing.T) {
	a, _ := withCategories(t)
	if err := promote(a, "patch-errata", sat); err != nil {
		t.Fatal(err)
	}
	d, _ := a.readGateway("patch-pilot")
	first := d.Categories["patch-errata"].Promotion
	// The gateway still has the first promotion.
	a.OPAData = func(string) (any, error) {
		return map[string]any{"mode": "supervised", "paused": false, "permissions": []any{1, 2, 3},
			"categories": map[string]any{"patch-errata": map[string]any{"mode": "bounded", "promotion": first}}}, nil
	}
	a.PolicyWait = 0
	n := 0
	a.Secret = func() string { n++; return fmt.Sprintf("%032d", n) }
	sun := "windows:\n  - {days: [sun], from: '02:00', to: '04:00', tz: Europe/Berlin}\n"
	if err := promote(a, "patch-errata", sun); err == nil || !strings.Contains(err.Error(), "did not load the change") {
		t.Errorf("returned while the gateway had the old envelope: %v", err)
	}
}

// journal is an agent's records and the gateway's decisions as
// journalctl -o json gives them, for agent-admin readiness.
func readinessJournal(n int, declined int, start time.Time) func(args ...string) ([]byte, error) {
	var agent, gw []string
	line := func(m map[string]any) string { b, _ := json.Marshal(m); return string(b) }
	us := func(t time.Time) string { return strconv.FormatInt(t.UnixMicro(), 10) }
	for i := 0; i < n; i++ {
		at := start.Add(time.Duration(i) * time.Hour)
		inv, id := fmt.Sprintf("e-%d", i), fmt.Sprintf("a-%d", i)
		agent = append(agent,
			line(map[string]any{"AGENT_EVENT": "tool.call", "AGENT_INVOCATION": inv, "AGENT_CALL_ID": "c1", "AGENT_TOOL": "zypp__install_patches", "AGENT_ARGS_SHA256": "x", "__REALTIME_TIMESTAMP": us(at)}),
			line(map[string]any{"AGENT_EVENT": "approval.pending", "AGENT_INVOCATION": inv, "AGENT_CALL_ID": "c1", "AGENT_TOOL": "zypp__install_patches", "AGENT_APPROVAL_ID": id, "__REALTIME_TIMESTAMP": us(at.Add(time.Second))}),
			line(map[string]any{"AGENT_EVENT": "tool.result", "AGENT_INVOCATION": inv, "AGENT_CALL_ID": "c1", "AGENT_OUTCOME": "ok", "__REALTIME_TIMESTAMP": us(at.Add(2 * time.Minute))}))
		msg, _ := json.Marshal(map[string]any{"msg": "mcp", "event": "mcp-approval", "id": id, "ok": i >= declined, "by": "alice", "principal": "agent-patch-pilot"})
		gw = append(gw, line(map[string]any{"MESSAGE": string(msg), "__REALTIME_TIMESTAMP": us(at.Add(time.Minute))}))
	}
	return func(args ...string) ([]byte, error) {
		for _, a := range args {
			if a == "_SYSTEMD_UNIT=mcp-gateway.service" {
				return []byte(strings.Join(gw, "\n")), nil
			}
		}
		return []byte(strings.Join(agent, "\n")), nil
	}
}

func TestReadinessReport(t *testing.T) {
	a, _ := withCategories(t)
	start := a.Now().AddDate(0, 0, -20)
	a.JournalJSON = readinessJournal(60, 4, start)
	r, err := a.Readiness("patch-pilot")
	if err != nil {
		t.Fatal(err)
	}
	pe := r.Categories["patch-errata"]
	if pe.Proposals != 60 || pe.Declined != 4 || pe.Approved != 56 || pe.Ready || !strings.Contains(strings.Join(pe.Unmet, ";"), "override rate 6.7 % above 5.0 %") {
		t.Errorf("patch-errata %+v", pe)
	}
	if cl, ok := r.Categories["config-lifecycle"]; !ok || cl.Proposals != 0 {
		t.Errorf("config-lifecycle %+v (every category of the permissions)", cl)
	}
	if r.Thresholds != readiness.Defaults {
		t.Errorf("thresholds %+v", r.Thresholds)
	}
	// The site's thresholds: 4 of 60 overrides is within 10 %.
	host.WriteFile(a.Path(PromotionConfig), []byte("override_rate: 0.1\nproposals: 40\n"), 0o644)
	r, _ = a.Readiness("patch-pilot", "patch-errata")
	if pe := r.Categories["patch-errata"]; !pe.Ready || r.Thresholds.Proposals != 40 || len(r.Categories) != 1 {
		t.Errorf("with the site's thresholds: %+v %+v", r.Thresholds, pe)
	}
}

func TestThresholdsFile(t *testing.T) {
	a, _ := withCategories(t)
	for body, says := range map[string]string{
		"override_rate: 5\n":  "fractions from 0 to 1",
		"window_days: 0\n":    "window_days 1 or more",
		"overide_rate: 0.1\n": "overide_rate",
	} {
		host.WriteFile(a.Path(PromotionConfig), []byte(body), 0o644)
		if _, err := a.Thresholds(); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%q: %v, want %q", body, err, says)
		}
	}
}

// A ready category is promoted without --despite-readiness, and the
// record carries the measures.
func TestPromoteWhenReady(t *testing.T) {
	a, mem := withCategories(t)
	a.JournalJSON = readinessJournal(60, 0, a.Now().AddDate(0, 0, -20))
	if err := a.Promote("patch-pilot", "patch-errata", "alice", Promotion{Envelope: []byte(sat)}); err != nil {
		t.Fatal(err)
	}
	r := mem.Events(journal.CategoryPromoted)
	var m readiness.Measures
	if len(r) != 1 || json.Unmarshal([]byte(r[0].Fields["READINESS"]), &m) != nil || !m.Ready || m.Proposals != 60 {
		t.Errorf("record %v", r)
	}
}

// withDiagnostics adds n diagnosis reports to a readiness journal, each
// with one hypothesis and root's feedback: the first confirmed ones
// confirmed, the others rejected.
func withDiagnostics(read func(...string) ([]byte, error), at time.Time, n, confirmed int) func(...string) ([]byte, error) {
	var lines []string
	line := func(m map[string]any) string { b, _ := json.Marshal(m); return string(b) }
	for i := 0; i < n; i++ {
		inv, us := fmt.Sprintf("d-%d", i), strconv.FormatInt(at.Add(time.Duration(i)*time.Minute).UnixMicro(), 10)
		verdict := "rejected"
		if i < confirmed {
			verdict = "confirmed"
		}
		lines = append(lines,
			line(map[string]any{"AGENT_EVENT": "diagnosis.report", "AGENT_INVOCATION": inv, "_UID": "981", "__REALTIME_TIMESTAMP": us,
				"AGENT_REPORT": `{"hypotheses":[{"id":"h1"}],"evidence":[{"verified":true}]}`}),
			line(map[string]any{"AGENT_EVENT": "diagnosis.feedback", "AGENT_INVOCATION": inv, "_UID": "0", "_COMM": "agent-admin", "__REALTIME_TIMESTAMP": us,
				"AGENT_FEEDBACK": `{"hypotheses":{"h1":"` + verdict + `"}}`}))
	}
	return func(args ...string) ([]byte, error) {
		b, err := read(args...)
		for _, a := range args {
			if a == "_SYSTEMD_UNIT=mcp-gateway.service" {
				return b, err
			}
		}
		return append(append(b, '\n'), []byte(strings.Join(lines, "\n"))...), err
	}
}

// On a host that started diagnostics-only, the agent's diagnostics
// history is part of every category's readiness (§6b); elsewhere not.
func TestReadinessCountsDiagnostics(t *testing.T) {
	a, _ := withCategories(t)
	start := a.Now().AddDate(0, 0, -20)
	ready := readinessJournal(60, 0, start) // every other threshold met
	a.JournalJSON = ready
	r, _ := a.Readiness("patch-pilot", "patch-errata")
	if !r.Categories["patch-errata"].Ready || r.DiagnosticsApply {
		t.Fatalf("without an enablement state: %+v", r)
	}

	host.WriteFile(a.Path(GatewayEnablement), []byte("_enablement:\n  state: categories\n  enabled: [diagnostics, patch-errata, config-lifecycle]\n"), 0o644)
	r, _ = a.Readiness("patch-pilot", "patch-errata")
	pe := r.Categories["patch-errata"]
	if pe.Ready || !r.DiagnosticsApply || strings.Join(pe.Unmet, "; ") != "diagnosis reports with feedback 0 of 10; no hypothesis confirmed or rejected yet" {
		t.Errorf("no history: %+v", pe)
	}
	a.JournalJSON = withDiagnostics(ready, start, 10, 6)
	r, _ = a.Readiness("patch-pilot", "patch-errata")
	if pe := r.Categories["patch-errata"]; pe.Ready || strings.Join(pe.Unmet, "; ") != "confirmed hypotheses 60.0 % below 70.0 %" {
		t.Errorf("60 %% confirmed: %+v", pe)
	}
	a.JournalJSON = withDiagnostics(ready, start, 10, 8)
	if r, _ = a.Readiness("patch-pilot", "patch-errata"); !r.Categories["patch-errata"].Ready {
		t.Errorf("80 %% confirmed of 10: %+v", r.Categories["patch-errata"])
	}
	// promote follows readiness.
	a.JournalJSON = withDiagnostics(ready, start, 10, 6)
	if err := a.Promote("patch-pilot", "patch-errata", "alice", Promotion{Envelope: []byte(sat)}); err == nil || !strings.Contains(err.Error(), "confirmed hypotheses 60.0 %") {
		t.Errorf("promote: %v", err)
	}
	// The site turns them off.
	host.WriteFile(a.Path(PromotionConfig), []byte("diagnostics_feedback: 0\ndiagnostics_confirmed_rate: 0\n"), 0o644)
	if r, _ = a.Readiness("patch-pilot", "patch-errata"); !r.Categories["patch-errata"].Ready {
		t.Errorf("turned off: %+v", r.Categories["patch-errata"])
	}
	for _, bad := range []string{"diagnostics_feedback: -1\n", "diagnostics_confirmed_rate: 1.5\n"} {
		host.WriteFile(a.Path(PromotionConfig), []byte(bad), 0o644)
		if _, err := a.Readiness("patch-pilot"); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The emergency stops are read for readiness: root's stop during an
// approved call's activity is an incident; a stop written as the agent is
// none (O11).
func TestReadinessCountsTheEmergencyStop(t *testing.T) {
	a, _ := withCategories(t)
	start := a.Now().AddDate(0, 0, -20)
	read := readinessJournal(60, 0, start)
	stop := func(at time.Time, uid string) string {
		b, _ := json.Marshal(map[string]any{"AGENT_EVENT": "agents.stopped", "SYSLOG_IDENTIFIER": "custos-promotion", "_UID": uid, "_COMM": "agent-admin",
			"__REALTIME_TIMESTAMP": strconv.FormatInt(at.UnixMicro(), 10)})
		return string(b)
	}
	var asked [][]string
	a.JournalJSON = func(args ...string) ([]byte, error) {
		if slices.Contains(args, "AGENT_EVENT=agents.stopped") {
			asked = append(asked, args)
			return []byte(stop(start.Add(90*time.Second), "0") + "\n" + stop(start.Add(5*time.Hour+90*time.Second), "981")), nil
		}
		return read(args...)
	}
	r, err := a.Readiness("patch-pilot", "patch-errata")
	if err != nil {
		t.Fatal(err)
	}
	if pe := r.Categories["patch-errata"]; pe.Approved != 60 || pe.Incidents != 1 {
		t.Errorf("approved %d, incidents %d; want 60, 1", pe.Approved, pe.Incidents)
	}
	if len(asked) != 1 || !slices.Contains(asked[0], "_UID=0") || !slices.Contains(asked[0], "SYSLOG_IDENTIFIER=custos-promotion") {
		t.Errorf("stop records read with %q", asked)
	}
}
