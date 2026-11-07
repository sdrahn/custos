package admin

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sdrahn/custos/internal/journal"
)

// fakeJournal holds an agent's records and the gateway's decisions, as
// journalctl -o json gives them.
type fakeJournal struct{ agent, gw []string }

// rootsEvent: the records agent-admin writes, as root.
func rootsEvent(e string) bool {
	return e == "category.review" || e == "category.promoted" || e == "category.demoted" || e == "categories.enabled" || e == "diagnosis.feedback"
}

func (j *fakeJournal) rec(at time.Time, event string, f map[string]string) {
	m := map[string]string{"AGENT_EVENT": event, "__REALTIME_TIMESTAMP": strconv.FormatInt(at.UnixMicro(), 10)}
	for k, v := range f {
		m["AGENT_"+k] = v
	}
	if rootsEvent(event) {
		m["_UID"] = "0" // agent-admin's records are root's (journald's trusted field)
		m["_COMM"] = "agent-admin"
	} else {
		m["_UID"] = "981" // the agent's
	}
	b, _ := json.Marshal(m)
	j.agent = append(j.agent, string(b))
}

func (j *fakeJournal) decision(at time.Time, id string, ok bool) {
	msg, _ := json.Marshal(map[string]any{"msg": "mcp", "event": "mcp-approval", "id": id, "ok": ok, "by": "alice", "principal": "agent-patch-pilot"})
	b, _ := json.Marshal(map[string]string{"MESSAGE": string(msg), "__REALTIME_TIMESTAMP": strconv.FormatInt(at.UnixMicro(), 10)})
	j.gw = append(j.gw, string(b))
}

// result: the gateway's mcp-result of a call it forwarded (R2), with the
// progress token the agent sent.
func (j *fakeJournal) result(at time.Time, f map[string]any) {
	m := map[string]any{"audit": true, "msg": "mcp", "event": "mcp-result", "sub": "agent-patch-pilot", "server": "zypp",
		"name": "install_patches", "duration_ms": 900}
	for k, v := range f {
		m[k] = v
	}
	msg, _ := json.Marshal(m)
	b, _ := json.Marshal(map[string]string{"MESSAGE": string(msg), "__REALTIME_TIMESTAMP": strconv.FormatInt(at.UnixMicro(), 10)})
	j.gw = append(j.gw, string(b))
}

// review: a verdict of the gateway's approvers on a call that ran (R4).
func (j *fakeJournal) review(at time.Time, id, verdict, reason string) {
	msg, _ := json.Marshal(map[string]any{"audit": true, "msg": "mcp", "event": "mcp-review", "id": id, "sub": "agent-patch-pilot",
		"server": "zypp", "name": "install_patches", "verdict": verdict, "reason": reason, "by": "alice"})
	b, _ := json.Marshal(map[string]string{"MESSAGE": string(msg), "__REALTIME_TIMESTAMP": strconv.FormatInt(at.UnixMicro(), 10)})
	j.gw = append(j.gw, string(b))
}

// proposal: a call of the category that waited for an approval, decided ok.
func (j *fakeJournal) proposal(at time.Time, inv, id string, ok bool) {
	f := map[string]string{"INVOCATION": inv, "CALL_ID": "c1", "TOOL": "zypp__install_patches"}
	j.rec(at, "tool.call", f)
	j.rec(at.Add(time.Second), "approval.pending", map[string]string{"INVOCATION": inv, "CALL_ID": "c1", "TOOL": "zypp__install_patches", "APPROVAL_ID": id})
	j.decision(at.Add(time.Minute), id, ok)
}

// autonomous: a call of the category that ran without an approval.
func (j *fakeJournal) autonomous(at time.Time, inv, outcome string) {
	f := map[string]string{"INVOCATION": inv, "CALL_ID": "c1", "TOOL": "zypp__install_patches"}
	j.rec(at, "tool.call", f)
	r := map[string]string{"INVOCATION": inv, "CALL_ID": "c1", "TOOL": "zypp__install_patches", "OUTCOME": outcome}
	if outcome == "error" {
		r["REASON"] = "zypper: conflict"
	}
	j.rec(at.Add(time.Second), "tool.result", r)
}

func (j *fakeJournal) read(args ...string) ([]byte, error) {
	for _, a := range args {
		if a == "_SYSTEMD_UNIT=mcp-gateway.service" {
			return []byte(strings.Join(j.gw, "\n")), nil
		}
	}
	return []byte(strings.Join(j.agent, "\n")), nil
}

// promoted: patch-pilot with patch-errata promoted a day before now, with
// the given rollback, and the journal j.
func promoted(t *testing.T, rollback string) (*Admin, *journal.Memory, *fakeJournal, *rec) {
	t.Helper()
	a, mem, r := withCategoriesRec(t)
	// Promoted at the test clock, then evaluated a day later.
	if err := promote(a, "patch-errata", sat+rollback); err != nil {
		t.Fatal(err)
	}
	mem.Records = nil
	day := a.Now().Add(24 * time.Hour)
	a.Now = func() time.Time { return day }
	j := &fakeJournal{}
	a.JournalJSON = j.read
	return a, mem, j, r
}

func evaluate(t *testing.T, a *Admin) []Demotion {
	t.Helper()
	d, err := a.Evaluate()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func mode(a *Admin, cat string) string {
	d, _ := a.readGateway("patch-pilot")
	if c := d.Categories[cat]; c != nil {
		return c.Mode
	}
	return ""
}

func TestPromoteEnablesTheEvaluator(t *testing.T) {
	_, _, _, r := promoted(t, "")
	if !strings.Contains(strings.Join(r.cmds, "\n"), "systemctl enable --now custos-demote.timer") {
		t.Errorf("custos-demote.timer not enabled: %q", r.cmds)
	}
}

func TestNothingToDemote(t *testing.T) {
	a, mem, j, _ := promoted(t, "rollback: {on: [tool_error, limit_exceeded]}\n")
	at := a.Now().Add(-time.Hour)
	j.autonomous(at, "e-1", "ok")
	j.proposal(at, "e-2", "a-2", true)
	if d := evaluate(t, a); len(d) != 0 || mode(a, "patch-errata") != "bounded" || len(mem.Records) != 0 {
		t.Errorf("demoted %v, records %v", d, mem.Records)
	}
}

func TestDemotesOnTheOverrideRate(t *testing.T) {
	a, mem, j, _ := promoted(t, "")
	at := a.Now().Add(-time.Hour)
	// Declined before the promotion: the supervised period, not counted.
	j.proposal(a.Now().Add(-48*time.Hour), "e-0", "a-0", false)
	if d := evaluate(t, a); len(d) != 0 {
		t.Fatalf("demoted for a decline before the promotion: %v", d)
	}
	// Outside its windows, a call of the bounded category asks, and is declined.
	j.proposal(at, "e-1", "a-1", false)
	d := evaluate(t, a)
	if len(d) != 1 || d[0].Category != "patch-errata" || !strings.Contains(d[0].Reason, "override rate 100.0 % above 5.0 %") {
		t.Fatalf("demotions %v", d)
	}
	if mode(a, "patch-errata") != "supervised" {
		t.Error("not demoted in the gateway data")
	}
	r := mem.Events(journal.CategoryDemoted)
	if len(r) != 1 || r[0].Fields["BY"] != DemoteBy || !strings.Contains(r[0].Fields["REASON"], "override rate") {
		t.Errorf("record %v", r)
	}
	// Demoted once: a supervised category is not evaluated.
	if d := evaluate(t, a); len(d) != 0 {
		t.Errorf("demoted again: %v", d)
	}
}

func TestRollbackToolError(t *testing.T) {
	a, _, j, _ := promoted(t, "rollback: {on: [tool_error]}\n")
	at := a.Now().Add(-time.Hour)
	// An error of a call that was approved is not the promotion's.
	j.proposal(at, "e-1", "a-1", true)
	j.rec(at.Add(2*time.Minute), "tool.result", map[string]string{"INVOCATION": "e-1", "CALL_ID": "c1", "TOOL": "zypp__install_patches", "APPROVAL_ID": "a-1", "OUTCOME": "error"})
	if d := evaluate(t, a); len(d) != 0 {
		t.Fatalf("demoted for an approved call's error: %v", d)
	}
	j.autonomous(at.Add(10*time.Minute), "e-2", "error")
	d := evaluate(t, a)
	if len(d) != 1 || !strings.Contains(d[0].Reason, "rollback tool_error: zypp__install_patches failed") || !strings.Contains(d[0].Reason, "zypper: conflict") {
		t.Fatalf("demotions %v", d)
	}
}

// The gateway's outcome (R2): an autonomous call that failed demotes
// although the agent recorded it as ok, or not at all. An approved or
// granted call's failure is not the promotion's; another agent's is not
// this one's.
func TestRollbackToolErrorFromTheGateway(t *testing.T) {
	a, _, j, _ := promoted(t, "rollback: {on: [tool_error, limit_exceeded]}\n")
	at := a.Now().Add(-time.Hour)
	j.result(at, map[string]any{"outcome": "tool_error", "approval_id": "a-1"})
	j.result(at, map[string]any{"outcome": "error", "grant": "g-1"})
	j.result(at, map[string]any{"outcome": "tool_error", "sub": "agent-other"})
	j.result(at, map[string]any{"outcome": "tool_error", "server": "fs", "name": "write_file"})
	j.result(at, map[string]any{"outcome": "cancelled"})
	j.result(at.Add(-48*time.Hour), map[string]any{"outcome": "tool_error"}) // before the promotion
	j.autonomous(at.Add(time.Minute), "e-2", "ok")                           // the agent says ok
	j.result(at.Add(time.Minute), map[string]any{"outcome": "ok", "progress_token": "custos:e-2/c1"})
	if d := evaluate(t, a); len(d) != 0 {
		t.Fatalf("demoted for another call's failure: %v", d)
	}
	j.result(at.Add(10*time.Minute), map[string]any{"outcome": "tool_error", "error": "zypper exited 4", "progress_token": "custos:e-3/c1"})
	d := evaluate(t, a)
	if len(d) != 1 || !strings.Contains(d[0].Reason, "rollback tool_error: zypp__install_patches failed") ||
		!strings.Contains(d[0].Reason, "the gateway: tool_error: zypper exited 4") {
		t.Fatalf("demotions %v", d)
	}
}

// limit_exceeded joins the gateway's autonomous calls to the agent's
// activities by the progress token.
func TestRollbackLimitWithTheGatewaysCall(t *testing.T) {
	a, _, j, _ := promoted(t, "rollback: {on: [limit_exceeded]}\n")
	at := a.Now().Add(-time.Hour)
	j.result(at, map[string]any{"outcome": "ok", "progress_token": "custos:e-9/c1"})
	j.rec(at.Add(time.Minute), "limit.exceeded", map[string]string{"INVOCATION": "e-9", "REASON": "max_tool_calls=40"})
	d := evaluate(t, a)
	if len(d) != 1 || !strings.Contains(d[0].Reason, "rollback limit_exceeded: max_tool_calls=40 in activity e-9") {
		t.Fatalf("demotions %v", d)
	}
}

func TestRollbackOnlyWhatIsListed(t *testing.T) {
	a, _, j, _ := promoted(t, "rollback: {on: [limit_exceeded]}\n")
	at := a.Now().Add(-time.Hour)
	j.autonomous(at, "e-1", "error")
	if d := evaluate(t, a); len(d) != 0 {
		t.Fatalf("demoted for tool_error, which is not listed: %v", d)
	}
	j.autonomous(at.Add(time.Minute), "e-2", "ok")
	j.rec(at.Add(2*time.Minute), "limit.exceeded", map[string]string{"INVOCATION": "e-2", "REASON": "max_tool_calls=40"})
	d := evaluate(t, a)
	if len(d) != 1 || !strings.Contains(d[0].Reason, "rollback limit_exceeded: max_tool_calls=40 in activity e-2") {
		t.Fatalf("demotions %v", d)
	}
}

func TestRollbackHealthCheck(t *testing.T) {
	a, _, _, _ := promoted(t, "rollback: {on: [health_check_failed], health_check: 'systemctl is-system-running --quiet'}\n")
	var ran []string
	healthy := true
	a.HealthCheck = func(cmd string) error {
		ran = append(ran, cmd)
		if healthy {
			return nil
		}
		return errors.New("exit status 1")
	}
	if d := evaluate(t, a); len(d) != 0 || len(ran) != 1 || ran[0] != "systemctl is-system-running --quiet" {
		t.Fatalf("healthy: demoted %v, ran %q", d, ran)
	}
	healthy = false
	d := evaluate(t, a)
	if len(d) != 1 || d[0].Reason != `rollback health_check_failed: "systemctl is-system-running --quiet": exit status 1` {
		t.Fatalf("demotions %v", d)
	}
}

func TestRealHealthCheck(t *testing.T) {
	if err := runHealthCheck("true"); err != nil {
		t.Error(err)
	}
	if err := runHealthCheck("echo broken >&2; exit 3"); err == nil || !strings.Contains(err.Error(), "exit status 3: broken") {
		t.Errorf("%v", err)
	}
}

// A proposal declined in the same second just before the promotion is the
// supervised period's: the promotion time is kept to the fraction of the
// second (as the functional tests found, demoting at once for 100 %).
func TestDeclineInTheSecondBeforeThePromotion(t *testing.T) {
	a, _, _ := withCategoriesRec(t)
	at := time.Date(2026, 11, 3, 12, 0, 0, 900_000_000, time.UTC)
	a.Now = func() time.Time { return at }
	j := &fakeJournal{}
	a.JournalJSON = j.read
	f := map[string]string{"INVOCATION": "e-1", "CALL_ID": "c1", "TOOL": "zypp__install_patches"}
	j.rec(at.Add(-600*time.Millisecond), "tool.call", f)
	j.rec(at.Add(-500*time.Millisecond), "approval.pending", map[string]string{"INVOCATION": "e-1", "CALL_ID": "c1",
		"TOOL": "zypp__install_patches", "APPROVAL_ID": "a-1"})
	j.decision(at.Add(-400*time.Millisecond), "a-1", false)
	if err := promote(a, "patch-errata", sat); err != nil {
		t.Fatal(err)
	}
	d, _ := a.readGateway("patch-pilot")
	if pb := d.Categories["patch-errata"].PromotedBy; pb != "alice 2026-11-03T12:00:00.9Z" {
		t.Errorf("promoted_by %q", pb)
	}
	later := at.Add(time.Hour)
	a.Now = func() time.Time { return later }
	if d := evaluate(t, a); len(d) != 0 || mode(a, "patch-errata") != "bounded" {
		t.Errorf("demoted for a decline before the promotion: %v", d)
	}
}

// An approver's incident verdict on the gateway (R4) demotes, as root's
// review does; an ok, or one before the promotion, does not.
func TestDemotesOnTheGatewaysReview(t *testing.T) {
	a, _, j, _ := promoted(t, "")
	j.review(a.Now().Add(-48*time.Hour), "r-0", "incident", "before the promotion")
	j.review(a.Now().Add(-time.Hour), "r-1", "ok", "")
	if d := evaluate(t, a); len(d) != 0 {
		t.Fatalf("demoted: %v", d)
	}
	j.review(a.Now().Add(-time.Minute), "r-2", "incident", "patched the database host")
	d := evaluate(t, a)
	if len(d) != 1 || !strings.Contains(d[0].Reason, "review by alice on the gateway: zypp__install_patches (review r-2) should not have run: patched the database host") {
		t.Fatalf("demotions %v", d)
	}
}

// capped: the gateway's decision record of a call it denied over a rate
// limit (its rate_limit obligation).
func (j *fakeJournal) capped(at time.Time, f map[string]any) {
	m := map[string]any{"audit": true, "msg": "mcp", "sub": "agent-patch-pilot", "action": "tools.call", "server": "zypp",
		"name": "install_patches", "effect": "deny", "reason": "rate limit exceeded"}
	for k, v := range f {
		m[k] = v
	}
	msg, _ := json.Marshal(m)
	b, _ := json.Marshal(map[string]string{"MESSAGE": string(msg), "__REALTIME_TIMESTAMP": strconv.FormatInt(at.UnixMicro(), 10)})
	j.gw = append(j.gw, string(b))
}

// Calls reached the envelope's rate three times within a day (O12): asked
// for an approval (over_rate) or denied by a rate_limit.
func TestDemotesOnRepeatedRateCaps(t *testing.T) {
	a, mem, j, _ := promoted(t, "rate: {calls: 2, per: 1m}\n")
	now := a.Now()
	j.capped(now.Add(-time.Hour), nil)
	j.capped(now.Add(-50*time.Minute), nil)
	// Not counted: before the day, another agent, another category's tool,
	// another reason, not the gateway's audit, an allowed call.
	j.capped(now.Add(-25*time.Hour), nil)
	j.capped(now.Add(-time.Minute), map[string]any{"sub": "agent-other"})
	j.capped(now.Add(-time.Minute), map[string]any{"server": "systemd", "name": "restart_unit"})
	j.capped(now.Add(-time.Minute), map[string]any{"reason": "denied by policy"})
	j.capped(now.Add(-time.Minute), map[string]any{"audit": false})
	j.capped(now.Add(-time.Minute), map[string]any{"effect": "allow"})
	if d := evaluate(t, a); len(d) != 0 {
		t.Fatalf("demoted on two caps: %v", d)
	}
	// The third: over the envelope's rate, the gateway asked for an
	// approval (approval_over_rate, its R6), which was given.
	j.capped(now.Add(-10*time.Minute), map[string]any{"effect": "allow", "reason": "", "over_rate": "2/m"})
	d := evaluate(t, a)
	if len(d) != 1 || !strings.Contains(d[0].Reason, "rate (2 calls per 1m per tool) reached 3 times within a day: zypp__install_patches at ") {
		t.Fatalf("demotions %v", d)
	}
	if mode(a, "patch-errata") != "supervised" {
		t.Error("not demoted")
	}
	if r := mem.Events(journal.CategoryDemoted); len(r) != 1 || r[0].Fields["BY"] != DemoteBy {
		t.Errorf("records: %v", r)
	}
}

// Caps without a rate in the envelope (a rate_limit the permission sets
// itself) are no reason to demote.
func TestRateCapsWithoutAnEnvelopeRate(t *testing.T) {
	a, _, j, _ := promoted(t, "")
	for i := range 5 {
		j.capped(a.Now().Add(-time.Duration(i+1)*time.Minute), nil)
	}
	if d := evaluate(t, a); len(d) != 0 {
		t.Fatalf("demotions %v", d)
	}
}
