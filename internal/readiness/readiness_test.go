package readiness

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

var perms = []map[string]any{
	{"server": "zypp", "tool": "list_*", "read_only": true, "category": "patch-errata"},
	{"server": "zypp", "tool": "patch", "category": "patch-errata"},
	{"server": "systemd", "tool": "restart_unit", "category": "config-lifecycle"},
	{"server": "fs", "tool": "write_file"},
}

// log builds an agent's records; each call is one proposal of tool.
type log struct {
	recs      []Record
	decisions map[string]Decision
	n         int
}

func (l *log) rec(at time.Time, event string, f map[string]string) {
	r := Record{"AGENT_EVENT": event, "__REALTIME_TIMESTAMP": strconv.FormatInt(at.UnixMicro(), 10)}
	for k, v := range f {
		r["AGENT_"+k] = v
	}
	r["_UID"] = "981" // the agent's
	if event == "category.review" {
		r["_UID"] = "0" // agent-admin's, as root
		r["_COMM"] = "agent-admin"
	}
	l.recs = append(l.recs, r)
}

// proposal: a call of tool in invocation inv at, its approval item, and
// the gateway's decision (decided false: none, it expired).
func (l *log) proposal(at time.Time, tool, args string, decided, ok bool) (inv, id string) {
	l.n++
	inv, call, id := fmt.Sprintf("e-%d", l.n), "c1", fmt.Sprintf("a-%d", l.n)
	f := map[string]string{"INVOCATION": inv, "CALL_ID": call, "TOOL": tool, "ARGS_SHA256": args}
	l.rec(at, "tool.call", f)
	l.rec(at.Add(time.Second), "approval.pending", map[string]string{"INVOCATION": inv, "CALL_ID": call, "TOOL": tool, "APPROVAL_ID": id})
	if decided {
		if l.decisions == nil {
			l.decisions = map[string]Decision{}
		}
		l.decisions[id] = Decision{OK: ok, By: "alice", At: at.Add(time.Minute)}
	}
	return inv, id
}

func (l *log) result(at time.Time, inv, outcome string) {
	l.rec(at, "tool.result", map[string]string{"INVOCATION": inv, "CALL_ID": "c1", "OUTCOME": outcome})
	l.rec(at.Add(time.Second), "invocation.end", map[string]string{"INVOCATION": inv})
}

func TestMeasures(t *testing.T) {
	var l log
	// 10 patches, approved and run, the first 20 days ago.
	for i := 0; i < 10; i++ {
		at := t0.Add(time.Duration(i) * time.Hour)
		inv, _ := l.proposal(at, "zypp__patch", fmt.Sprintf("args-%d", i%3), true, true)
		l.result(at.Add(2*time.Minute), inv, "ok")
	}
	// One declined.
	l.proposal(t0.Add(20*time.Hour), "zypp__patch", "args-0", true, false)
	// One approved, then cancelled before it ran.
	inv, _ := l.proposal(t0.Add(21*time.Hour), "zypp__patch", "args-1", true, true)
	l.rec(t0.Add(21*time.Hour+2*time.Minute), "activity.cancel", map[string]string{"INVOCATION": inv})
	// One approved that failed: an incident.
	inv, _ = l.proposal(t0.Add(22*time.Hour), "zypp__patch", "args-2", true, true)
	l.result(t0.Add(22*time.Hour+2*time.Minute), inv, "error")
	// One approved whose activity reached a limit: an incident.
	inv, _ = l.proposal(t0.Add(23*time.Hour), "zypp__patch", "args-0", true, true)
	l.rec(t0.Add(23*time.Hour+90*time.Second), "limit.exceeded", map[string]string{"INVOCATION": inv})
	// One approved during which the agent was paused: an incident.
	inv, _ = l.proposal(t0.Add(24*time.Hour), "zypp__patch", "args-1", true, true)
	l.rec(t0.Add(24*time.Hour+90*time.Second), "agent.paused", nil)
	l.result(t0.Add(24*time.Hour+3*time.Minute), inv, "ok")
	// Not counted: expired (no decision), another category, a tool no
	// category's permission matches, a read-only tool of the category.
	l.proposal(t0.Add(25*time.Hour), "zypp__patch", "args-0", false, false)
	l.proposal(t0.Add(26*time.Hour), "systemd__restart_unit", "x", true, false)
	l.proposal(t0.Add(27*time.Hour), "fs__write_file", "x", true, false)
	l.proposal(t0.Add(28*time.Hour), "zypp__list_updates", "x", true, false)

	now := t0.Add(20 * 24 * time.Hour)
	m := Measure("patch-errata", Now(perms), l.recs, Audit{Decisions: l.decisions}, Defaults, now)
	want := Measures{Proposals: 15, Approved: 14, Declined: 1, Cancelled: 1, Overrides: 2, Incidents: 3, Tools: 1, Calls: 3}
	if m.Proposals != want.Proposals || m.Approved != want.Approved || m.Declined != want.Declined || m.Cancelled != want.Cancelled ||
		m.Overrides != want.Overrides || m.Incidents != want.Incidents || m.Tools != want.Tools || m.Calls != want.Calls {
		t.Fatalf("measures %+v\nwant      %+v", m, want)
	}
	if fmt.Sprintf("%.4f %.4f", m.OverrideRate, m.IncidentRate) != fmt.Sprintf("%.4f %.4f", 2.0/15, 3.0/13) {
		t.Errorf("rates %v %v", m.OverrideRate, m.IncidentRate)
	}
	if m.ObservedDays < 19.9 || m.ObservedDays > 20.1 || m.FirstProposal != "2026-11-01T09:00:01Z" {
		t.Errorf("observed %v since %s", m.ObservedDays, m.FirstProposal)
	}
	if m.Ready {
		t.Error("ready with 15 proposals and 13 % overrides")
	}
	got := strings.Join(m.Unmet, "; ")
	for _, s := range []string{"15 proposals of 50", "override rate 13.3 % above 5.0 %", "incident rate 23.1 % above 2.0 %"} {
		if !strings.Contains(got, s) {
			t.Errorf("unmet %q, want %q", got, s)
		}
	}
	if strings.Contains(got, "observed") {
		t.Errorf("20 days observed reported unmet: %q", got)
	}
	// The other category's own measures.
	c := Measure("config-lifecycle", Now(perms), l.recs, Audit{Decisions: l.decisions}, Defaults, now)
	if c.Proposals != 1 || c.Declined != 1 || c.OverrideRate != 1 {
		t.Errorf("config-lifecycle %+v", c)
	}
}

func TestReady(t *testing.T) {
	var l log
	for i := 0; i < 60; i++ {
		at := t0.Add(time.Duration(i) * 6 * time.Hour)
		inv, _ := l.proposal(at, "zypp__patch", "a", true, true)
		l.result(at.Add(time.Minute+time.Second), inv, "ok")
	}
	m := Measure("patch-errata", Now(perms), l.recs, Audit{Decisions: l.decisions}, Defaults, t0.Add(16*24*time.Hour))
	if !m.Ready || len(m.Unmet) != 0 || m.Proposals != 60 || m.OverrideRate != 0 || m.IncidentRate != 0 {
		t.Errorf("%+v", m)
	}
	// Too recent for the observation period.
	m = Measure("patch-errata", Now(perms), l.recs, Audit{Decisions: l.decisions}, Thresholds{ObservationDays: 30, Proposals: 50, OverrideRate: 0.05, IncidentRate: 0.02}, t0.Add(16*24*time.Hour))
	if m.Ready || !strings.Contains(strings.Join(m.Unmet, ";"), "observed 16.0 days of 30") {
		t.Errorf("%+v", m)
	}
}

func TestEmpty(t *testing.T) {
	m := Measure("patch-errata", Now(perms), nil, Audit{Decisions: nil}, Defaults, t0)
	if m.Ready || m.Proposals != 0 || m.OverrideRate != 0 || m.FirstProposal != "" {
		t.Errorf("%+v", m)
	}
	b, _ := json.Marshal(m)
	if strings.Contains(string(b), "0001-01-01") {
		t.Errorf("zero time in %s", b)
	}
}

func TestParse(t *testing.T) {
	agent := `{"AGENT_EVENT":"approval.pending","AGENT_APPROVAL_ID":"a-1","__REALTIME_TIMESTAMP":"1762000000000000","_PID":"12"}
not json
{"AGENT_EVENT":"tool.result","AGENT_OUTCOME":"ok","__REALTIME_TIMESTAMP":"1762000001000000"}`
	r := ParseRecords([]byte(agent))
	if len(r) != 2 || r[0]["AGENT_APPROVAL_ID"] != "a-1" || !At(r[1]).Equal(time.UnixMicro(1762000001000000)) {
		t.Errorf("%v", r)
	}
	msg := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		o, _ := json.Marshal(map[string]string{"MESSAGE": string(b), "__REALTIME_TIMESTAMP": "1762000002000000"})
		return string(o)
	}
	gw := strings.Join([]string{
		msg(map[string]any{"msg": "mcp", "event": "mcp-approval", "id": "a-1", "ok": true, "by": "alice", "principal": "agent-pp"}),
		msg(map[string]any{"msg": "mcp", "event": "mcp-approval", "id": "a-2", "ok": false, "by": "bob", "principal": "agent-pp"}),
		msg(map[string]any{"msg": "mcp", "event": "mcp-approval", "id": "a-3", "ok": true, "by": "bob", "principal": "agent-other"}),
		msg(map[string]any{"msg": "mcp", "event": "mcp-grant-revoke", "id": "g-1", "ok": true, "principal": "agent-pp"}),
		`{"MESSAGE":"time=… level=INFO msg=\"session started\""}`,
	}, "\n")
	d := ParseDecisions([]byte(gw), "agent-pp")
	if len(d) != 2 || !d["a-1"].OK || d["a-1"].By != "alice" || d["a-2"].OK || d["a-1"].At.IsZero() {
		t.Errorf("%v", d)
	}
}

func TestCategories(t *testing.T) {
	if got := strings.Join(Categories(perms), ","); got != "config-lifecycle,patch-errata" {
		t.Errorf("%s", got)
	}
}

func (l *log) auto(at time.Time, inv, tool, outcome string) {
	l.rec(at, "tool.call", map[string]string{"INVOCATION": inv, "CALL_ID": "c1", "TOOL": tool, "ARGS_SHA256": "h-" + inv})
	l.rec(at.Add(time.Second), "tool.result", map[string]string{"INVOCATION": inv, "CALL_ID": "c1", "TOOL": tool, "OUTCOME": outcome})
}

func (l *log) review(at time.Time, cat, inv, verdict string) {
	l.rec(at, "category.review", map[string]string{"CATEGORY": cat, "INVOCATION": inv, "CALL_ID": "c1", "VERDICT": verdict, "BY": "alice"})
}

func TestAutonomous(t *testing.T) {
	var l log
	l.auto(t0, "e-old", "zypp__patch", "ok") // before the promotion
	since := t0.Add(time.Hour)
	l.auto(since.Add(time.Hour), "x-1", "zypp__patch", "ok")
	l.auto(since.Add(2*time.Hour), "x-2", "zypp__patch", "error")
	l.auto(since.Add(3*time.Hour), "x-3", "systemd__restart_unit", "ok") // another category
	l.auto(since.Add(4*time.Hour), "x-4", "zypp__list_updates", "ok")    // read-only
	inv, _ := l.proposal(since.Add(5*time.Hour), "zypp__patch", "x", true, true)
	l.result(since.Add(5*time.Hour+2*time.Minute), inv, "ok") // approved, not autonomous
	got := Autonomous("patch-errata", Now(perms), l.recs, since)
	if len(got) != 2 || got[0].Activity != "x-1" || got[1].Activity != "x-2" || got[1].Outcome != "error" || got[0].Args != "h-x-1" || got[0].Time == "" {
		t.Fatalf("%+v", got)
	}
}

func TestReviewsCountAsOverrides(t *testing.T) {
	var l log
	for i := 0; i < 8; i++ {
		inv, _ := l.proposal(t0.Add(time.Duration(i)*time.Hour), "zypp__patch", "a", true, true)
		l.result(t0.Add(time.Duration(i)*time.Hour+2*time.Minute), inv, "ok")
	}
	l.auto(t0.Add(10*time.Hour), "e-a", "zypp__patch", "ok")
	l.auto(t0.Add(11*time.Hour), "e-b", "zypp__patch", "ok")
	l.review(t0.Add(12*time.Hour), "patch-errata", "e-a", VerdictOK)
	l.review(t0.Add(13*time.Hour), "patch-errata", "e-b", VerdictIncident)
	l.review(t0.Add(13*time.Hour), "config-lifecycle", "e-b", VerdictIncident) // another category's
	m := Measure("patch-errata", Now(perms), l.recs, Audit{Decisions: l.decisions}, Defaults, t0.Add(20*24*time.Hour))
	if m.Proposals != 8 || m.Reviewed != 2 || m.ReviewOverrides != 1 || m.Overrides != 1 || fmt.Sprintf("%.3f", m.OverrideRate) != "0.100" {
		t.Errorf("%+v", m)
	}
	// A later verdict replaces an earlier one.
	l.review(t0.Add(14*time.Hour), "patch-errata", "e-b", VerdictOK)
	if r := Reviews("patch-errata", l.recs); r["e-b c1"].Verdict != VerdictOK || len(r) != 2 {
		t.Errorf("%+v", r)
	}
}

func TestPendingSample(t *testing.T) {
	now := t0.Add(30 * 24 * time.Hour)
	mk := func(n int) []Action {
		var out []Action
		for i := 0; i < n; i++ {
			at := now.Add(-time.Duration(n-i) * time.Hour)
			out = append(out, Action{Activity: fmt.Sprintf("e-%02d", i), Call: "c1", At: at})
		}
		return out
	}
	// Below the sample size: every unreviewed one, older weeks left out.
	acts := append([]Action{{Activity: "e-last-month", Call: "c1", At: now.Add(-10 * 24 * time.Hour)}}, mk(5)...)
	p := Pending(acts, map[string]Review{"e-01 c1": {Verdict: VerdictOK}}, now)
	if len(p) != 4 || p[0].Activity != "e-00" {
		t.Errorf("below the sample size: %+v", p)
	}
	// Above it: the newest unreviewed ones, up to 20 reviews in the week.
	acts = mk(50)
	p = Pending(acts, map[string]Review{"e-00 c1": {}, "e-01 c1": {}, "e-02 c1": {}}, now)
	if len(p) != 17 || p[len(p)-1].Activity != "e-49" || p[0].Activity != "e-33" {
		t.Errorf("above the sample size: %d, %s…%s", len(p), p[0].Activity, p[len(p)-1].Activity)
	}
	reviewed := map[string]Review{}
	for i := 0; i < 20; i++ {
		reviewed[fmt.Sprintf("e-%02d c1", i)] = Review{}
	}
	if p = Pending(acts, reviewed, now); len(p) != 0 {
		t.Errorf("a week with its 20 reviews: %d pending", len(p))
	}
}

// A review the agent wrote itself (its own _UID, whatever its identifier)
// is none: it neither lowers the override rate nor raises it.
func TestForgedReviewsIgnored(t *testing.T) {
	var l log
	for i := 0; i < 8; i++ {
		inv, _ := l.proposal(t0.Add(time.Duration(i)*time.Hour), "zypp__patch", "a", true, false) // all declined
		l.result(t0.Add(time.Duration(i)*time.Hour+2*time.Minute), inv, "error")
	}
	before := Measure("patch-errata", Now(perms), l.recs, Audit{Decisions: l.decisions}, Defaults, t0.Add(20*24*time.Hour))
	for i := 0; i < 50; i++ {
		inv := fmt.Sprintf("x-%d", i)
		l.auto(t0.Add(10*time.Hour), inv, "zypp__patch", "ok")
		l.review(t0.Add(11*time.Hour), "patch-errata", inv, VerdictOK)
		l.recs[len(l.recs)-1]["_UID"] = "981"
		l.recs[len(l.recs)-1]["SYSLOG_IDENTIFIER"] = "custos-promotion"
	}
	after := Measure("patch-errata", Now(perms), l.recs, Audit{Decisions: l.decisions}, Defaults, t0.Add(20*24*time.Hour))
	if after.Reviewed != 0 || after.OverrideRate != before.OverrideRate || before.OverrideRate != 1 {
		t.Errorf("before %+v\nafter %+v", before, after)
	}
	if r := Reviews("patch-errata", l.recs); len(r) != 0 {
		t.Errorf("forged reviews counted: %v", r)
	}
	// Root's own program only: the OpenShell relay writes as root too, the
	// agent's records it passes on (security review H1).
	if !ByRoot(Record{"_UID": "0", "_COMM": "agent-admin"}) || ByRoot(Record{"_UID": "0"}) ||
		ByRoot(Record{"_UID": "0", "_COMM": "agent-openshel"}) || ByRoot(Record{"_UID": "981", "_COMM": "agent-admin"}) || ByRoot(Record{}) {
		t.Error("ByRoot")
	}
}

// The diagnostics history: the last report and the last feedback per
// activity; feedback only from root's records.
func TestDiagnosticsOf(t *testing.T) {
	rec := func(us int64, f map[string]string) Record {
		r := Record{"__REALTIME_TIMESTAMP": strconv.FormatInt(us, 10)}
		for k, v := range f {
			r[k] = v
		}
		return r
	}
	report := func(hyps, verified, unverified int) string {
		var h, e []string
		for i := 0; i < hyps; i++ {
			h = append(h, `{"id":"h`+strconv.Itoa(i)+`"}`)
		}
		for i := 0; i < verified; i++ {
			e = append(e, `{"verified":true}`)
		}
		for i := 0; i < unverified; i++ {
			e = append(e, `{"verified":false}`)
		}
		return `{"hypotheses":[` + strings.Join(h, ",") + `],"evidence":[` + strings.Join(e, ",") + `]}`
	}
	recs := []Record{
		// e-1: reported twice; the later report counts.
		rec(1, map[string]string{"AGENT_EVENT": "diagnosis.report", "AGENT_INVOCATION": "e-1", "AGENT_REPORT": report(5, 0, 5)}),
		rec(2, map[string]string{"AGENT_EVENT": "diagnosis.report", "AGENT_INVOCATION": "e-1", "AGENT_REPORT": report(2, 2, 1)}),
		rec(3, map[string]string{"AGENT_EVENT": "diagnosis.report", "AGENT_INVOCATION": "e-2", "AGENT_REPORT": report(1, 1, 0)}),
		rec(4, map[string]string{"AGENT_EVENT": "diagnosis.report", "AGENT_INVOCATION": "e-3", "AGENT_REPORT": report(1, 0, 1)}),
		// Feedback on e-1, replaced by a later one.
		rec(5, map[string]string{"AGENT_EVENT": "diagnosis.feedback", "AGENT_INVOCATION": "e-1", "_UID": "0", "_COMM": "agent-admin",
			"AGENT_FEEDBACK": `{"hypotheses":{"h0":"rejected","h1":"rejected"}}`}),
		rec(6, map[string]string{"AGENT_EVENT": "diagnosis.feedback", "AGENT_INVOCATION": "e-1", "_UID": "0", "_COMM": "agent-admin",
			"AGENT_FEEDBACK": `{"hypotheses":{"h0":"confirmed","h1":"unsure"},"plan":"useful","executed":true}`}),
		rec(7, map[string]string{"AGENT_EVENT": "diagnosis.feedback", "AGENT_INVOCATION": "e-2", "_UID": "0", "_COMM": "agent-admin",
			"AGENT_FEEDBACK": `{"hypotheses":{"h0":"rejected"},"plan":"not_useful"}`}),
		// Written by the agent itself (its own uid): not feedback.
		rec(8, map[string]string{"AGENT_EVENT": "diagnosis.feedback", "AGENT_INVOCATION": "e-3", "_UID": "461",
			"AGENT_FEEDBACK": `{"hypotheses":{"h0":"confirmed"},"plan":"useful","executed":true}`}),
	}
	d := DiagnosticsOf(recs)
	want := Diagnostics{Reports: 3, Hypotheses: 4, Quotes: 5, VerifiedQuotes: 3, WithFeedback: 2, Confirmed: 1, Rejected: 1, Unsure: 1,
		PlansUseful: 1, PlansNotUseful: 1, PlansExecuted: 1, ConfirmedRate: 0.5}
	if d != want {
		t.Errorf("got  %+v\nwant %+v", d, want)
	}
}

// The gateway's outcomes (R2): an approved call that failed is an
// incident though the agent recorded it as ok, or not at all; one the
// gateway saw cancelled is cancelled.
func TestIncidentsFromTheGateway(t *testing.T) {
	var l log
	for i := 0; i < 4; i++ {
		at := t0.Add(time.Duration(i) * time.Hour)
		inv, id := l.proposal(at, "zypp__patch", "x", true, true)
		d := l.decisions[id]
		switch i {
		case 0:
			l.result(at.Add(2*time.Minute), inv, "ok") // the agent says ok
			d.Outcome = "tool_error"
		case 1: // no record of the agent's
			d.Outcome = "timeout"
		case 2:
			l.result(at.Add(2*time.Minute), inv, "ok")
			d.Outcome = "cancelled"
		case 3:
			l.result(at.Add(2*time.Minute), inv, "ok")
			d.Outcome = "ok"
		}
		l.decisions[id] = d
	}
	m := Measure("patch-errata", Now(perms), l.recs, Audit{Decisions: l.decisions}, Defaults, t0.Add(20*24*time.Hour))
	if m.Approved != 4 || m.Cancelled != 1 || m.Incidents != 2 {
		t.Errorf("%+v", m)
	}
}

func TestParseResults(t *testing.T) {
	msg := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		o, _ := json.Marshal(map[string]string{"MESSAGE": string(b), "__REALTIME_TIMESTAMP": "1762000002000000"})
		return string(o)
	}
	gw := strings.Join([]string{
		msg(map[string]any{"audit": true, "msg": "mcp", "event": "mcp-approval", "id": "a-1", "ok": true, "by": "alice", "principal": "agent-pp"}),
		msg(map[string]any{"audit": true, "msg": "mcp", "event": "mcp-result", "sub": "agent-pp", "server": "zypp", "name": "patch",
			"approval_id": "a-1", "outcome": "tool_error", "error": "exit 4", "progress_token": "custos:e-1/c2", "duration_ms": 10}),
		msg(map[string]any{"audit": true, "msg": "mcp", "event": "mcp-result", "sub": "agent-pp", "server": "zypp", "name": "list", "outcome": "ok"}),
		msg(map[string]any{"audit": true, "msg": "mcp", "event": "mcp-result", "sub": "agent-other", "server": "zypp", "name": "patch", "outcome": "error"}),
		msg(map[string]any{"msg": "mcp", "event": "mcp-result", "sub": "agent-pp", "server": "zypp", "name": "patch", "outcome": "error"}), // no audit record
	}, "\n")
	rs := ParseResults([]byte(gw), "agent-pp")
	if len(rs) != 2 || rs[0].Tool != "zypp__patch" || rs[0].Outcome != "tool_error" || rs[0].Error != "exit 4" || rs[0].ApprovalID != "a-1" ||
		rs[0].Invocation != "e-1" || rs[0].CallID != "c2" || rs[1].Invocation != "" || rs[0].At.IsZero() {
		t.Errorf("%+v", rs)
	}
	if d := ParseDecisions([]byte(gw), "agent-pp"); d["a-1"].Outcome != "tool_error" || !d["a-1"].OK {
		t.Errorf("%+v", d)
	}
	for o, want := range map[string]bool{"tool_error": true, "error": true, "timeout": true, "cancelled": false, "ok": false, "": false} {
		if Failed(o) != want {
			t.Errorf("Failed(%q)", o)
		}
	}
}

// The gateway's approvers' reviews (mcp-review, R4): a later verdict on
// the same item replaces an earlier one; they count for the category of
// their tool, as root's do.
func TestGatewayReviews(t *testing.T) {
	msg := func(at time.Time, m map[string]any) string {
		m["audit"], m["msg"], m["event"] = true, "mcp", "mcp-review"
		b, _ := json.Marshal(m)
		o, _ := json.Marshal(map[string]string{"MESSAGE": string(b), "__REALTIME_TIMESTAMP": strconv.FormatInt(at.UnixMicro(), 10)})
		return string(o)
	}
	gw := strings.Join([]string{
		msg(t0, map[string]any{"id": "r-1", "sub": "agent-pp", "server": "zypp", "name": "patch", "verdict": "ok", "by": "alice"}),
		msg(t0.Add(time.Hour), map[string]any{"id": "r-1", "sub": "agent-pp", "server": "zypp", "name": "patch", "verdict": "incident", "reason": "wrong host", "by": "bob"}),
		msg(t0, map[string]any{"id": "r-2", "sub": "agent-pp", "server": "zypp", "name": "patch", "verdict": "ok", "by": "alice"}),
		msg(t0, map[string]any{"id": "r-3", "sub": "agent-pp", "server": "systemd", "name": "restart_unit", "verdict": "incident", "reason": "x", "by": "alice"}),
		msg(t0, map[string]any{"id": "r-4", "sub": "agent-other", "server": "zypp", "name": "patch", "verdict": "incident", "reason": "x", "by": "alice"}),
	}, "\n")
	rv := ParseReviews([]byte(gw), "agent-pp")
	if len(rv) != 3 || rv[2].Item != "r-1" || rv[2].Verdict != "incident" || rv[2].By != "bob" || rv[2].Tool != "zypp__patch" || rv[2].Reason != "wrong host" {
		t.Fatalf("%+v", rv)
	}
	if a := ParseAudit([]byte(gw), "agent-pp"); len(a.Reviews) != 3 {
		t.Errorf("audit %+v", a)
	}
	m := Measure("patch-errata", Now(perms), nil, Audit{Reviews: rv}, Defaults, t0.Add(24*time.Hour))
	if m.Reviewed != 2 || m.ReviewOverrides != 1 || m.Overrides != 1 || m.OverrideRate != 0.5 {
		t.Errorf("%+v", m)
	}
}

// The actions to review from the gateway's records (R2): every call it
// let through without an approval or a grant, though the agent recorded
// none, with the decision id; the agent's own records only add.
func TestAutonomousActionsFromTheGateway(t *testing.T) {
	var l log
	since := t0
	// The agent recorded one action (e-1) and left out e-2.
	l.rec(t0.Add(time.Hour), "tool.call", map[string]string{"INVOCATION": "e-1", "CALL_ID": "c1", "TOOL": "zypp__patch", "ARGS_SHA256": "aa"})
	l.rec(t0.Add(time.Hour), "tool.result", map[string]string{"INVOCATION": "e-1", "CALL_ID": "c1", "TOOL": "zypp__patch", "OUTCOME": "ok"})
	l.rec(t0.Add(3*time.Hour), "tool.call", map[string]string{"INVOCATION": "e-9", "CALL_ID": "c1", "TOOL": "zypp__patch"})
	l.rec(t0.Add(3*time.Hour), "tool.result", map[string]string{"INVOCATION": "e-9", "CALL_ID": "c1", "TOOL": "zypp__patch", "OUTCOME": "ok"})
	results := []Result{
		{Tool: "zypp__patch", Outcome: "ok", DecisionID: "d-1", Invocation: "e-1", CallID: "c1", At: t0.Add(time.Hour)},
		{Tool: "zypp__patch", Outcome: "tool_error", DecisionID: "d-2", Invocation: "e-2", CallID: "c1", At: t0.Add(2 * time.Hour)},
		{Tool: "zypp__patch", Outcome: "ok", DecisionID: "d-3", At: t0.Add(150 * time.Minute)},                           // no token
		{Tool: "zypp__patch", Outcome: "ok", DecisionID: "d-4", ApprovalID: "a-1", At: t0.Add(time.Hour)},                // approved
		{Tool: "zypp__patch", Outcome: "ok", DecisionID: "d-5", Grant: "g-1", At: t0.Add(time.Hour)},                     // a grant
		{Tool: "zypp__list_updates", Outcome: "ok", DecisionID: "d-6", At: t0.Add(time.Hour)},                            // read-only
		{Tool: "zypp__patch", Outcome: "ok", DecisionID: "d-7", Invocation: "e-0", CallID: "c1", At: t0.Add(-time.Hour)}, // before
	}
	acts := AutonomousActions("patch-errata", Now(perms), l.recs, results, since)
	var got []string
	for _, a := range acts {
		got = append(got, a.Activity+"/"+a.Decision+"/"+a.Outcome+"/"+a.Args)
	}
	if strings.Join(got, " ") != "e-1/d-1/ok/aa e-2/d-2/error/ /d-3/ok/ e-9//ok/" {
		t.Fatalf("%v", got)
	}
	// Reviewed: root's by activity and call, the gateway's by decision id.
	reviews := ReviewMap("patch-errata", Now(perms), nil, []Review{{Item: "r-1", Tool: "zypp__patch", Decision: "d-3", Verdict: "ok"}})
	reviews["e-1 c1"] = Review{Verdict: "ok"}
	var open []string
	for _, a := range Pending(acts, reviews, t0.Add(24*time.Hour)) {
		open = append(open, a.Activity+a.Decision)
	}
	if strings.Join(open, " ") != "e-2d-2 e-9" {
		t.Errorf("pending %v", open)
	}
}

// A call reviewed by root and on the gateway is one review: the later.
func TestOneReviewPerCall(t *testing.T) {
	var l log
	l.rec(t0, "category.review", map[string]string{"CATEGORY": "patch-errata", "INVOCATION": "e-1", "CALL_ID": "c1", "VERDICT": "ok", "DECISION_ID": "d-1"})
	gw := []Review{
		{Item: "r-1", Tool: "zypp__patch", Decision: "d-1", Verdict: "incident", Reason: "x", At: t0.Add(time.Hour)},
		{Item: "r-2", Tool: "zypp__patch", Decision: "d-2", Verdict: "ok", At: t0},
	}
	all := AllReviews("patch-errata", Now(perms), l.recs, gw)
	if len(all) != 2 || all[1].Item != "r-1" || all[1].Verdict != "incident" {
		t.Errorf("%+v", all)
	}
}

// An emergency stop during an approved call's activity is an incident,
// when root recorded it (O11).
func TestEmergencyStopIsAnIncident(t *testing.T) {
	var l log
	inv, _ := l.proposal(t0, "zypp__patch", "a", true, true)
	stop := Record{"AGENT_EVENT": AgentsStopped, "_UID": "0", "_COMM": "agent-admin", "__REALTIME_TIMESTAMP": strconv.FormatInt(t0.Add(90*time.Second).UnixMicro(), 10)}
	l.recs = append(l.recs, stop)
	l.result(t0.Add(3*time.Minute), inv, "ok")
	// After its activity ended: none.
	inv, _ = l.proposal(t0.Add(time.Hour), "zypp__patch", "b", true, true)
	l.result(t0.Add(time.Hour+2*time.Minute), inv, "ok")
	late := Record{"AGENT_EVENT": AgentsStopped, "_UID": "0", "_COMM": "agent-admin", "__REALTIME_TIMESTAMP": strconv.FormatInt(t0.Add(2*time.Hour).UnixMicro(), 10)}
	l.recs = append(l.recs, late)
	// The agent's own record of a stop: none.
	inv, _ = l.proposal(t0.Add(3*time.Hour), "zypp__patch", "c", true, true)
	forged := Record{"AGENT_EVENT": AgentsStopped, "_UID": "981", "__REALTIME_TIMESTAMP": strconv.FormatInt(t0.Add(3*time.Hour+90*time.Second).UnixMicro(), 10)}
	l.recs = append(l.recs, forged)
	l.result(t0.Add(3*time.Hour+3*time.Minute), inv, "ok")
	m := Measure("patch-errata", Now(perms), l.recs, Audit{Decisions: l.decisions}, Defaults, t0.Add(24*time.Hour))
	if m.Approved != 3 || m.Incidents != 1 {
		t.Errorf("approved %d, incidents %d; want 3, 1", m.Approved, m.Incidents)
	}
}

// changed: root's record of a change of the permissions.
func changed(at time.Time, from, to []map[string]any, uid string) Record {
	f, _ := json.Marshal(from)
	t, _ := json.Marshal(to)
	return Record{"AGENT_EVENT": PermissionsChanged, "_UID": uid, "_COMM": "agent-admin", "AGENT_PERMISSIONS_FROM": string(f), "AGENT_PERMISSIONS_TO": string(t),
		"__REALTIME_TIMESTAMP": strconv.FormatInt(at.UnixMicro(), 10)}
}

// A call counts for the category its permissions had at the call's time,
// not the current ones (O11).
func TestPermissionsAtTheCallsTime(t *testing.T) {
	patchIn := []map[string]any{{"server": "zypp", "tool": "patch", "category": "patch-errata"}}
	patchOut := []map[string]any{{"server": "zypp", "tool": "patch", "category": "config-lifecycle"}}
	var l log
	// zypp__patch was config-lifecycle's until a day in; patch-errata's
	// since; config-lifecycle's again since day 3 (the current data).
	l.proposal(t0, "zypp__patch", "a", true, false)                   // config-lifecycle
	l.proposal(t0.Add(36*time.Hour), "zypp__patch", "b", true, false) // patch-errata
	l.proposal(t0.Add(80*time.Hour), "zypp__patch", "c", true, false) // config-lifecycle
	l.recs = append(l.recs, changed(t0.Add(24*time.Hour), patchOut, patchIn, "0"), changed(t0.Add(72*time.Hour), patchIn, patchOut, "0"))
	// The agent's own record of a change: none.
	l.recs = append(l.recs, changed(t0.Add(79*time.Hour), patchOut, patchIn, "981"))
	h := NewHistory(patchOut, l.recs)
	if len(h.Changes) != 2 {
		t.Fatalf("changes: %d", len(h.Changes))
	}
	now := t0.Add(5 * 24 * time.Hour)
	if m := Measure("patch-errata", h, l.recs, Audit{Decisions: l.decisions}, Defaults, now); m.Proposals != 1 || m.Declined != 1 {
		t.Errorf("patch-errata: %d proposals, %d declined; want 1, 1", m.Proposals, m.Declined)
	}
	if m := Measure("config-lifecycle", h, l.recs, Audit{Decisions: l.decisions}, Defaults, now); m.Proposals != 2 {
		t.Errorf("config-lifecycle: %d proposals; want 2", m.Proposals)
	}
	// With the current data only, all three were config-lifecycle's.
	if m := Measure("config-lifecycle", Now(patchOut), l.recs, Audit{Decisions: l.decisions}, Defaults, now); m.Proposals != 3 {
		t.Errorf("current data: %d proposals; want 3", m.Proposals)
	}
	// Before the first change read: what it changed from.
	if got := h.At(t0); len(got) != 1 || got[0]["category"] != "config-lifecycle" {
		t.Errorf("at t0: %v", got)
	}
}
