package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sdrahn/custos/internal/delegate"
	"github.com/sdrahn/custos/internal/governance"
	"github.com/sdrahn/custos/internal/journal"
	"github.com/sdrahn/custos/internal/playbook"
	"github.com/sdrahn/custos/internal/testutil"
	"google.golang.org/genai"
)

func playbooks(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	put := func(name, s string) {
		f := filepath.Join(root, playbook.Vendor, name+".yaml")
		os.MkdirAll(filepath.Dir(f), 0o755)
		os.WriteFile(f, []byte(s), 0o644)
	}
	// fs__read_file is a gateway tool the agent is not shown (its
	// configuration names only the server zypp).
	put("updates", "updates:\n  title: Updates\n  steps:\n    - {tool: zypp__list_updates, why: Pending updates.}\n"+
		"    - {tool: fs__read_file, why: A file., optional: true}\n  look_for: Security patches.\n")
	put("files", "files:\n  title: Files\n  steps:\n    - {tool: fs__read_file, why: A file.}\n  look_for: Anything.\n")
	put("broken", "broken: {title: x}\n")
	put("unit-updates", "unit-updates:\n  title: Updates of a unit\n  steps:\n    - {tool: zypp__list_updates, why: All., args: {all: true}}\n"+
		"    - {tool: zypp__list_updates, why: The unit's., args: {for: [\"{unit}\"], limit: 5}}\n  look_for: Patches.\n")
	return root
}

// An activity runs a playbook: the host makes the steps' calls (those
// offered), through the gateway and recorded like the model's, before
// the model's first turn; the model gets the steps, what to look for,
// the person's words and the steps' outputs. The activity and its
// record name the playbook.
func TestPlaybookActivity(t *testing.T) {
	e := newEnvPlaybooks(t, playbooks(t), testutil.Text("Report: nothing pending."))
	res, b := e.do("POST", "/v1/activities", "", map[string]any{"playbook": "updates", "prompt": "nginx in particular", "wait": true})
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, b)
	}
	var act governance.Activity
	json.Unmarshal(b, &act)
	if act.Playbook != "updates" || act.State != governance.StateFinished {
		t.Errorf("activity %s", b)
	}
	// It answered in prose, without a report: the host asked once more
	// (the report nudge, TestReportNudge).
	if len(e.m.Calls) != 2 || !act.ReportNudged {
		t.Fatalf("the model was called %d times (nudged %v), want twice: once for the report, once asked for it", len(e.m.Calls), act.ReportNudged)
	}
	cs := e.m.Calls[0].Contents
	sent := ""
	for _, c := range cs {
		for _, p := range c.Parts {
			if p.Text != "" && c.Role == "user" {
				sent = p.Text
			}
		}
	}
	for _, want := range []string{`Run the diagnostic playbook "updates"`, "1. zypp__list_updates: Pending updates.", "Look for: Security patches.",
		"The host calls these tools for you", "The administrator adds: nginx in particular"} {
		if !strings.Contains(sent, want) {
			t.Errorf("the model was not given %q:\n%s", want, sent)
		}
	}
	if strings.Contains(sent, "fs__read_file") {
		t.Errorf("an optional step not offered reached the model:\n%s", sent)
	}
	// The step's call and its output come before the model's turn.
	var call, out bool
	for _, c := range cs {
		for _, p := range c.Parts {
			if fc := p.FunctionCall; fc != nil && fc.Name == "zypp__list_updates" && len(fc.Args) == 0 {
				call = true
			}
			if fr := p.FunctionResponse; fr != nil && fr.Name == "zypp__list_updates" {
				out = true
			}
		}
	}
	if !call || !out {
		t.Errorf("the step's call (%v) or its output (%v) is not in the model's request", call, out)
	}
	if len(act.Calls) != 1 || act.Calls[0].Tool != "zypp__list_updates" || act.Calls[0].PlaybookStep != 1 || act.Calls[0].State != "ok" ||
		act.Calls[0].Rationale != "Playbook updates, step 1 of 1: Pending updates." {
		t.Errorf("calls %+v", act.Calls)
	}
	if n := len(e.gw.Ran()); n != 1 {
		t.Errorf("the gateway ran %d calls: %v", n, e.gw.Ran())
	}
	st := e.log.Events(journal.InvocationStart)
	if f := st[len(st)-1].Fields; f["PLAYBOOK"] != "updates" {
		t.Errorf("invocation.start %v", f)
	}
	tc := e.log.Events(journal.ToolCall)
	if len(tc) != 1 || tc[0].Fields["PLAYBOOK_STEP"] != "1" || tc[0].Fields["RATIONALE"] == "" {
		t.Errorf("tool.call %v", tc)
	}
	if n := len(e.log.Events(journal.ModelCall)); n != 2 {
		t.Errorf("%d model.call records, want 2 (the answer, and the one after the nudge): the host's turn is no model call", n)
	}
}

// A step's arguments go to the host's call; {unit} is the activity's
// unit, and a step that needs one runs only with it. A unit is a unit
// name, and only with a playbook.
func TestPlaybookUnit(t *testing.T) {
	stepArgs := func(cs []*genai.Content) []map[string]any {
		var out []map[string]any
		for _, c := range cs {
			for _, p := range c.Parts {
				if fc := p.FunctionCall; fc != nil && fc.Name == "zypp__list_updates" {
					out = append(out, fc.Args)
				}
			}
		}
		return out
	}
	e := newEnvPlaybooks(t, playbooks(t), testutil.Text("Report."), testutil.Text("Report."))
	res, b := e.do("POST", "/v1/activities", "", map[string]any{"playbook": "unit-updates", "unit": "nginx.service", "wait": true})
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, b)
	}
	got, _ := json.Marshal(stepArgs(e.m.Calls[0].Contents))
	if string(got) != `[{"all":true},{"for":["nginx.service"],"limit":5}]` {
		t.Errorf("the steps' arguments: %s", got)
	}
	sent := e.m.Calls[0].Contents[0].Parts[0].Text
	if !strings.Contains(sent, "The unit in question: nginx.service") || !strings.Contains(sent, `2. zypp__list_updates {"for":["nginx.service"],"limit":5}`) {
		t.Errorf("the model was not told the unit:\n%s", sent)
	}
	// Without a unit, the step that needs one does not run.
	e.do("POST", "/v1/activities", "", map[string]any{"playbook": "unit-updates", "wait": true})
	// (Calls[1] is the first activity's, after the report nudge.)
	if got, _ := json.Marshal(stepArgs(e.m.Calls[2].Contents)); string(got) != `[{"all":true}]` {
		t.Errorf("without a unit: %s", got)
	}
	for _, body := range []map[string]any{
		{"playbook": "unit-updates", "unit": "../../etc/passwd"},
		{"playbook": "unit-updates", "unit": "nginx"},
		{"prompt": "hello", "unit": "nginx.service"},
	} {
		if res, b := e.do("POST", "/v1/activities", "", body); res.StatusCode != 400 || !strings.Contains(string(b), "unit") {
			t.Errorf("%v: %d %s", body, res.StatusCode, b)
		}
	}
}

// A playbook run whose model answers without a report is asked once,
// in the same activity, to file one: the host's call of report_diagnosis
// after the model's text, which the host answers itself. A model that
// reports, an activity without a playbook, and a second answer without
// a report are not asked (again).
func TestReportNudge(t *testing.T) {
	report := reportArgs("high")
	e := newEnvPlaybooks(t, playbooks(t), testutil.Text("Nothing is pending, I think."), testutil.Call("r1", governance.ReportTool, report))
	_, b := e.do("POST", "/v1/activities", "", map[string]any{"playbook": "updates", "wait": true})
	var act governance.Activity
	json.Unmarshal(b, &act)
	if !act.ReportNudged || act.Report == nil || act.Report.Summary != "No updates are pending." || len(e.m.Calls) != 3 {
		t.Fatalf("nudged %v, report %+v, %d model calls: %s", act.ReportNudged, act.Report, len(e.m.Calls), b)
	}
	// The model saw its own text, the host's call and the reminder.
	var nudged bool
	for _, c := range e.m.Calls[1].Contents {
		for _, p := range c.Parts {
			if fr := p.FunctionResponse; fr != nil && fr.Name == governance.ReportTool && fr.Response["error"] == governance.ReportNudgeText {
				nudged = true
			}
		}
	}
	if !nudged {
		t.Errorf("the model was not asked for the report: %+v", e.m.Calls[1].Contents)
	}
	mc := e.log.Events(journal.ModelCall)
	if len(mc) != 3 || mc[0].Fields["REPORT_NUDGE"] != "1" || mc[1].Fields["REPORT_NUDGE"] != "" || mc[2].Fields["REPORT_NUDGE"] != "" {
		t.Errorf("model.call %+v", mc)
	}
	if len(act.Calls) != 1 {
		t.Errorf("the nudge is no tool call of the agent's: %+v", act.Calls)
	}

	// Asked once only: a second answer without a report ends the activity.
	e = newEnvPlaybooks(t, playbooks(t), testutil.Text("Nothing."), testutil.Text("Still nothing."))
	_, b = e.do("POST", "/v1/activities", "", map[string]any{"playbook": "updates", "wait": true})
	act = governance.Activity{}
	json.Unmarshal(b, &act)
	if !act.ReportNudged || act.Report != nil || act.State != governance.StateFinished || len(e.m.Calls) != 2 || act.Summary != "Still nothing." {
		t.Errorf("a second answer without a report: %d calls %s", len(e.m.Calls), b)
	}

	// A model that reports at once, and an activity without a playbook,
	// are not asked.
	e = newEnvPlaybooks(t, playbooks(t), testutil.Call("r1", governance.ReportTool, report), testutil.Text("Reported."))
	_, b = e.do("POST", "/v1/activities", "", map[string]any{"playbook": "updates", "wait": true})
	act = governance.Activity{}
	json.Unmarshal(b, &act)
	if act.ReportNudged || act.Report == nil || len(e.m.Calls) != 2 {
		t.Errorf("a model that reported: %d calls %s", len(e.m.Calls), b)
	}
	e = newEnvPlaybooks(t, playbooks(t), testutil.Text("Hello."))
	_, b = e.do("POST", "/v1/activities", "", map[string]any{"prompt": "hi", "wait": true})
	act = governance.Activity{}
	json.Unmarshal(b, &act)
	if act.ReportNudged || len(e.m.Calls) != 1 {
		t.Errorf("an activity without a playbook: %d calls %s", len(e.m.Calls), b)
	}
}

// A playbook is refused, before anything runs, when it is unknown,
// broken, or needs a tool the gateway does not offer the agent.
func TestPlaybookRefused(t *testing.T) {
	e := newEnvPlaybooks(t, playbooks(t))
	for _, c := range []struct {
		body map[string]any
		code int
		want string
	}{
		{map[string]any{"playbook": "files"}, 422, "needs tools the gateway does not offer agent patch-pilot: fs__read_file"},
		{map[string]any{"playbook": "no-such"}, 404, `no playbook "no-such"`},
		{map[string]any{"playbook": "broken"}, 400, "playbook broken"},
		{map[string]any{"playbook": "../etc"}, 400, "not a playbook name"},
		{map[string]any{}, 400, "playbook"},
	} {
		res, b := e.do("POST", "/v1/activities", "", c.body)
		if res.StatusCode != c.code || !strings.Contains(string(b), c.want) {
			t.Errorf("%v: %d %s", c.body, res.StatusCode, b)
		}
	}
	if n := len(e.log.Events(journal.InvocationStart)); n != 0 {
		t.Errorf("%d activities started", n)
	}
}

// GET /v1/playbooks says which playbooks this agent can run: those whose
// steps that are not optional all have tools the gateway offers it; the
// others with the tools they lack, a broken one with its error.
func TestPlaybooksOffered(t *testing.T) {
	e := newEnvPlaybooks(t, playbooks(t))
	res, b := e.do("GET", "/v1/playbooks", "", nil)
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, b)
	}
	var got struct {
		Playbooks map[string]PlaybookOffer `json:"playbooks"`
	}
	json.Unmarshal(b, &got)
	pb := got.Playbooks
	if u := pb["updates"]; !u.Runnable || u.Title != "Updates" || len(u.Missing) != 0 {
		t.Errorf("updates (its optional step not offered): %+v", u)
	}
	if f := pb["files"]; f.Runnable || len(f.Missing) != 1 || f.Missing[0] != "fs__read_file" {
		t.Errorf("files: %+v", f)
	}
	if br := pb["broken"]; br.Runnable || br.Error == "" {
		t.Errorf("broken: %+v", br)
	}
}

// An activity outlives the launcher: after an idle exit, the next one
// started on the same state directory lists it, with its report.
func TestActivityAfterRestart(t *testing.T) {
	e := newEnvPlaybooks(t, playbooks(t), testutil.Text("Report: nothing pending."))
	_, b := e.do("POST", "/v1/activities", "", map[string]any{"playbook": "updates", "wait": true})
	var act governance.Activity
	json.Unmarshal(b, &act)
	if act.ID == "" || act.State != governance.StateFinished {
		t.Fatalf("activity %s", b)
	}
	dir := filepath.Dir(e.gw.Socket)
	if _, err := os.Stat(filepath.Join(dir, governance.HistoryFile)); err != nil {
		t.Fatalf("not kept: %v", err)
	}
	l, err := New(context.Background(), Options{Config: e.l.cfg, Log: &journal.Memory{}, StateDir: dir, Model: &testutil.Model{}})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := l.Governor.Activity(act.ID)
	if !ok || a.Playbook != "updates" || a.State != governance.StateFinished {
		t.Errorf("after a restart: %+v %v", a, ok)
	}
}

// An activity keeps the model's last text in full as its response: the
// list has only its summary (the first 500 bytes), the activity itself
// the response, so a report with a table can be read whole.
func TestFullResponse(t *testing.T) {
	long := "| file system | used |\n|---|---|\n" + strings.Repeat("| /dev/vda2 | 98 % — ä |\n", 60)
	e := newEnvPlaybooks(t, playbooks(t), testutil.Text(long))
	_, b := e.do("POST", "/v1/activities", "", map[string]any{"prompt": "how full?", "wait": true})
	var act governance.Activity
	json.Unmarshal(b, &act)
	if act.Response != strings.TrimSpace(long) {
		t.Errorf("the ended activity's response is %d bytes, want %d", len(act.Response), len(long))
	}
	_, b = e.do("GET", "/v1/activities", "", nil)
	var list struct {
		Activities []governance.Activity `json:"activities"`
	}
	json.Unmarshal(b, &list)
	if len(list.Activities) != 1 || list.Activities[0].Response != "" || !strings.HasSuffix(list.Activities[0].Summary, "…") ||
		len(list.Activities[0].Summary) > governance.SummaryMax+len("…") || !utf8.ValidString(list.Activities[0].Summary) {
		t.Errorf("the list: %s", b)
	}
	_, b = e.do("GET", "/v1/activities/"+act.ID, "", nil)
	var one governance.Activity
	json.Unmarshal(b, &one)
	if one.Response != strings.TrimSpace(long) {
		t.Errorf("GET /v1/activities/{id}: the response is %d bytes, want %d", len(one.Response), len(long))
	}
}

// A delegation (§9) answers with the other agent's full response: the
// bridge's tool, against a running agent, returns all of a long answer,
// not the 500-byte summary.
func TestDelegationGetsFullResponse(t *testing.T) {
	long := "Restarted nginx.service; it is active.\n\n" + strings.Repeat("| nginx.service | active | 2026-11-07 |\n", 40)
	e := newEnvPlaybooks(t, "", testutil.Text(long))
	// The bridge finds agents as <dir>/<name>/api.sock.
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "patch-pilot"), 0o755)
	if err := os.Symlink(filepath.Join(filepath.Dir(e.gw.Socket), "api.sock"), filepath.Join(dir, "patch-pilot", "api.sock")); err != nil {
		t.Fatal(err)
	}
	r := delegate.Ask(context.Background(), dir, delegate.Args{Agent: "patch-pilot", Task: "Restart nginx."})
	var m map[string]any
	json.Unmarshal([]byte(r.Content[0].(*mcp.TextContent).Text), &m)
	if r.IsError || m["answer"] != strings.TrimSpace(long) || m["state"] != "finished" {
		t.Errorf("error %v, answer of %d bytes (want %d): %v", r.IsError, len(fmt.Sprint(m["answer"])), len(strings.TrimSpace(long)), m["state"])
	}
}
