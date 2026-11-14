package promotion

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sdrahn/custos/internal/journal"
)

// feed copies the promotion records the test's agent-admin wrote into the
// fake journal, as journald would hold them, so that reviews see them.
func feed(j *fakeJournal, mem *journal.Memory, at time.Time) {
	for _, r := range mem.Records {
		f := map[string]string{}
		for k, v := range r.Fields {
			f[k] = v
		}
		j.rec(at, r.Event, f)
	}
	mem.Records = nil
}

func TestReviewQueueAndVerdicts(t *testing.T) {
	a, mem, j, _ := promoted(t, "")
	at := a.Now().Add(-2 * time.Hour)
	j.autonomous(at, "e-1", "ok")
	j.autonomous(at.Add(time.Minute), "e-2", "ok")
	j.proposal(at.Add(2*time.Minute), "e-3", "a-3", true) // asked: not autonomous
	q, err := ReviewQueue(a, "patch-pilot")
	if err != nil {
		t.Fatal(err)
	}
	pe := q.Categories["patch-errata"]
	if pe.Mode != "bounded" || pe.Actions != 2 || pe.Reviewed != 0 || len(pe.Pending) != 2 || pe.Pending[0].Activity != "e-1" {
		t.Fatalf("queue %+v", q)
	}
	if _, ok := q.Categories["config-lifecycle"]; ok {
		t.Error("a category never promoted has no queue")
	}
	// ok: recorded, the category stays bounded.
	if err := Review(a, "patch-pilot", "patch-errata", "e-1", "c1", "ok", "alice", ""); err != nil {
		t.Fatal(err)
	}
	r := mem.Events(journal.CategoryReview)
	if len(r) != 1 || r[0].Fields["VERDICT"] != "ok" || r[0].Fields["BY"] != "alice" || r[0].Fields["INVOCATION"] != "e-1" ||
		r[0].Fields["TOOL"] != "zypp__install_patches" || r[0].Fields["CATEGORY"] != "patch-errata" {
		t.Fatalf("review record %v", r)
	}
	if mode(a, "patch-errata") != "bounded" {
		t.Error("an ok review demoted the category")
	}
	feed(j, mem, a.Now().Add(-time.Hour))
	q, _ = ReviewQueue(a, "patch-pilot", "patch-errata")
	if pe := q.Categories["patch-errata"]; pe.Reviewed != 1 || len(pe.Pending) != 1 || pe.Pending[0].Activity != "e-2" {
		t.Errorf("after one review %+v", pe)
	}
	// incident: needs a reason, demotes at once.
	if err := Review(a, "patch-pilot", "patch-errata", "e-2", "c1", "incident", "bob", ""); err == nil || !strings.Contains(err.Error(), "needs --reason") {
		t.Errorf("incident without a reason: %v", err)
	}
	if err := Review(a, "patch-pilot", "patch-errata", "e-2", "c1", "incident", "bob", "patched a production host at noon"); err != nil {
		t.Fatal(err)
	}
	if mode(a, "patch-errata") != "supervised" {
		t.Error("an incident did not demote the category")
	}
	if d := mem.Events(journal.CategoryDemoted); len(d) != 1 || d[0].Fields["BY"] != "bob" ||
		!strings.Contains(d[0].Fields["REASON"], "review: zypp__install_patches in activity e-2 should not have run: patched a production host at noon") {
		t.Errorf("demotion %v", d)
	}
}

func TestReviewRefuses(t *testing.T) {
	a, _, j, _ := promoted(t, "")
	j.autonomous(a.Now().Add(-time.Hour), "e-1", "ok")
	j.proposal(a.Now().Add(-time.Hour), "e-2", "a-2", true)
	for _, c := range []struct{ cat, act, verdict, says string }{
		{"patch-errata", "e-1", "maybe", `verdict "maybe": want ok or incident`},
		{"patch-errata", "e-9", "ok", "no action c1 of activity e-9 in category patch-errata ran without an approval"},
		{"patch-errata", "e-2", "ok", "no action c1 of activity e-2"}, // it had an approval
		{"config-lifecycle", "e-1", "ok", "category config-lifecycle has not been promoted"},
	} {
		if err := Review(a, "patch-pilot", c.cat, c.act, "c1", c.verdict, "alice", ""); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

// The evaluator demotes on an incident review, should the immediate
// demotion have failed.
// An incident review the agent wrote itself demotes nothing (and an "ok"
// it wrote would lower nothing): only root's reviews count.
func TestEvaluatorIgnoresForgedReview(t *testing.T) {
	a, _, j, _ := promoted(t, "")
	j.rec(a.Now().Add(-time.Hour), "category.review", map[string]string{"CATEGORY": "patch-errata", "INVOCATION": "e-1", "CALL_ID": "c1",
		"VERDICT": "incident", "BY": "bob", "REASON": "wrong host"})
	var m map[string]string
	json.Unmarshal([]byte(j.agent[len(j.agent)-1]), &m)
	m["_UID"] = "981"
	b, _ := json.Marshal(m)
	j.agent[len(j.agent)-1] = string(b)
	if d := evaluate(t, a); len(d) != 0 {
		t.Fatalf("a forged review demoted: %v", d)
	}
}

func TestEvaluatorDemotesOnAReview(t *testing.T) {
	a, _, j, _ := promoted(t, "")
	j.rec(a.Now().Add(-time.Hour), "category.review", map[string]string{"CATEGORY": "patch-errata", "INVOCATION": "e-1", "CALL_ID": "c1",
		"VERDICT": "incident", "BY": "bob", "REASON": "wrong host"})
	d := evaluate(t, a)
	if len(d) != 1 || d[0].Reason != "review by bob: the action in activity e-1 should not have run: wrong host" {
		t.Fatalf("demotions %v", d)
	}
}

// The queue from the gateway's records (R2): a call the agent left out of
// its records is listed all the same, with the gateway's decision id, and
// root's verdict names it; one reviewed on the gateway (R4) is reviewed.
func TestReviewQueueFromTheGateway(t *testing.T) {
	a, mem, j, _ := promoted(t, "")
	at := a.Now().Add(-2 * time.Hour)
	j.autonomous(at, "e-1", "ok")
	j.result(at, map[string]any{"outcome": "ok", "decision_id": "d-1", "progress_token": "custos:e-1/c1"})
	j.result(at.Add(time.Minute), map[string]any{"outcome": "ok", "decision_id": "d-2", "progress_token": "custos:e-2/c1"}) // the agent recorded none
	j.result(at.Add(2*time.Minute), map[string]any{"outcome": "ok", "decision_id": "d-3"})                                  // no token
	j.review(at.Add(3*time.Minute), "r-3", "ok", "")
	// r-3 reviewed d-3 on the gateway.
	j.gw[len(j.gw)-1] = strings.Replace(j.gw[len(j.gw)-1], `\"id\":\"r-3\"`, `\"id\":\"r-3\",\"decision_id\":\"d-3\"`, 1)
	q, err := ReviewQueue(a, "patch-pilot", "patch-errata")
	if err != nil {
		t.Fatal(err)
	}
	pe := q.Categories["patch-errata"]
	var got []string
	for _, x := range pe.Pending {
		got = append(got, x.Activity+"/"+x.Decision)
	}
	if pe.Actions != 3 || pe.Reviewed != 1 || strings.Join(got, " ") != "e-1/d-1 e-2/d-2" {
		t.Fatalf("queue %+v (%v)", pe, got)
	}
	if err := Review(a, "patch-pilot", "patch-errata", "e-2", "c1", "ok", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if r := mem.Events(journal.CategoryReview); len(r) != 1 || r[0].Fields["DECISION_ID"] != "d-2" || r[0].Fields["INVOCATION"] != "e-2" {
		t.Errorf("record %+v", r)
	}
	// An action without an activity is not root's to review here.
	if err := Review(a, "patch-pilot", "patch-errata", "", "", "ok", "alice", ""); err == nil || !strings.Contains(err.Error(), "reviewed on the gateway") {
		t.Errorf("%v", err)
	}
}
